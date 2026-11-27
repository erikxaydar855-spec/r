package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/pbkdf2"
)

const (
	MIN_VIDEOS_PER_DAY   = 3
	MIN_VIDEOS_PER_MONTH = 60
	MIN_VIDEOS_PER_YEAR  = 540
)

func rebind(query string) string {
	var result strings.Builder
	argNum := 1
	inSingle := false
	inDouble := false

	for i := 0; i < len(query); i++ {
		ch := query[i]

		if ch == '\'' && !inDouble {
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				result.WriteByte(ch)
				result.WriteByte(query[i+1])
				i++
				continue
			}
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		}

		if ch == '?' && !inSingle && !inDouble {
			fmt.Fprintf(&result, "$%d", argNum)
			argNum++
		} else {
			result.WriteByte(ch)
		}
	}

	return result.String()
}

type DB struct {
	*sql.DB
}

func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.DB.Exec(rebind(query), args...)
}

func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.DB.Query(rebind(query), args...)
}

func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.DB.QueryRow(rebind(query), args...)
}

type App struct {
	db        *DB
	templates *template.Template
	store     *sessions.CookieStore
}

type Video struct {
	ID           int
	Title        string
	Slug         string
	Body         string
	VideoURL     string
	Image        string
	Status       string
	CategoryID   int
	CategoryName string
	CreatedTime  string
}

type Category struct {
	ID   int
	Name string
}

type User struct {
	ID       int
	Username string
	Password string
	Email    string
	EmailVerified bool
	Region   string
	Role     string
	IsStaff  bool
	IsActive bool
}

type SchoolProfile struct {
	ID         int
	UserID     int
	SchoolName string
	Region     string
}

type ClassProfile struct {
	ID           int
	SchoolID     int
	ClassNumber  int
	ClassLetter  string
	ClassLeader  string
	ClassTeacher string
	ClassName    string
}

type Student struct {
	ID        int
	ClassID   int
	FullName  string
	BirthYear int
	Gender    string
	Address   string
	Origin    string
	CreatedAt string
}

type LessonStats struct {
	ID            int
	ClassID       int
	Hour          int
	Subject       string
	VideoID       int
	StudentsTotal int
	ActiveCount   int
	CreatedAt     string
}

type UserProgress struct {
	ID             int
	UserID         int
	VideoID        int
	WatchedPercent int
	UpdatedAt      string
}

// ===== TEMPLATE FUNCTIONS =====
// itubeYouTubeID accepts genuine YouTube video URLs only (not lookalike hosts).
func itubeYouTubeID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	var id string
	switch host {
	case "youtu.be", "www.youtu.be":
		id = strings.Trim(u.Path, "/")
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com":
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 1 && parts[0] == "watch" {
			id = u.Query().Get("v")
		}
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
			id = parts[1]
		}
	default:
		return "", false
	}
	if len(id) != 11 {
		return "", false
	}
	for _, r := range id {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return "", false
		}
	}
	return id, true
}

func isYouTube(raw string) bool { _, ok := itubeYouTubeID(raw); return ok }

func isVideoFile(url string) bool {
	ext := []string{".mp4", ".webm", ".ogg", ".mov", ".avi", ".mkv"}
	lower := strings.ToLower(url)
	for _, e := range ext {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

func extractYouTubeID(rawURL string) string {
	id, _ := itubeYouTubeID(rawURL)
	return id
}

func youtubeThumb(rawURL string) string {
	id := extractYouTubeID(rawURL)
	if id == "" {
		return ""
	}
	return "https://img.youtube.com/vi/" + id + "/hqdefault.jpg"
}

func toEmbedURL(rawURL string) string {
	id := extractYouTubeID(rawURL)
	var embedURL string
	if id != "" {
		embedURL = "https://www.youtube.com/embed/" + id
	} else {
		embedURL = rawURL
	}
	if strings.Contains(embedURL, "?") {
		embedURL += "&enablejsapi=1&rel=0"
	} else {
		embedURL += "?enablejsapi=1&rel=0"
	}
	return embedURL
}

func truncateString(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "Р Р†Р вЂљР’В¦"
}

func extractNumber(s string) string {
	var result strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// ===== OPENROUTER API =====
func callOpenRouterAPI(userQuery string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENROUTER_API_KEY environment variable not set")
	}
	reqBody := map[string]any{
		"model": "deepseek/deepseek-chat",
		"messages": []map[string]string{
			{"role": "system", "content": "Siz ITube platformasining o'zbek tilidagi ta'lim yordamchisisiz. Faqat o'zbek tilida, faqat ta'lim mavzusida to'liq va aniq javob bering."},
			{"role": "user", "content": userQuery},
		},
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "https://itube.uz")
	req.Header.Set("X-Title", "ITube AI")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenRouter API error (status %d): %s", resp.StatusCode, string(body))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "Javob topilmadi.", nil
	}
	return result.Choices[0].Message.Content, nil
}

// ===== MAIN =====
func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	rawDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer rawDB.Close()

	if err := rawDB.Ping(); err != nil {
		log.Fatal(err)
	}

	db := &DB{DB: rawDB}

	if err := ensureSchema(db); err != nil {
		log.Fatal(err)
	}
	if err := ensureAccountSchema(db); err != nil {
		log.Fatal(err)
	}
	tmpl, err := parseTemplates("templates")
	if err != nil {
		log.Fatal(err)
	}
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		secret = "dev-only-secret"
	}
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	app := &App{db: db, templates: tmpl, store: store}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	log.Printf("Go server running at http://127.0.0.1:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, app.routes()))
}

func parseTemplates(root string) (*template.Template, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".html") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no templates found")
	}
	sort.Strings(files)
	funcMap := template.FuncMap{
		"isYouTube":    isYouTube,
		"isVideoFile":  isVideoFile,
		"toEmbedURL":   toEmbedURL,
		"youtubeThumb": youtubeThumb,
		"truncate": func(n int, s string) string {
			return truncateString(s, n)
		},
		"now": time.Now,
	}
	return template.New("").Funcs(funcMap).ParseFiles(files...)
}

func ensureSchema(db *DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS auth_user (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			username TEXT UNIQUE,
			password TEXT,
			email TEXT,
			region TEXT,
			role TEXT DEFAULT 'user',
			is_staff INTEGER DEFAULT 0,
			is_active INTEGER DEFAULT 1
		);`,
		`CREATE TABLE IF NOT EXISTS news_app_category (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			name TEXT UNIQUE
		);`,
		`CREATE TABLE IF NOT EXISTS news_app_new_video (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			title TEXT,
			slug TEXT,
			body TEXT,
			video_url TEXT,
			image TEXT,
			status TEXT,
			category_id INTEGER,
			created_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (category_id) REFERENCES news_app_category(id)
		);`,
		`CREATE TABLE IF NOT EXISTS school_profile (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			user_id INTEGER UNIQUE,
			school_name TEXT,
			region TEXT,
			FOREIGN KEY (user_id) REFERENCES auth_user(id)
		);`,
		`CREATE TABLE IF NOT EXISTS class_profile (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			school_id INTEGER NOT NULL,
			class_number INTEGER NOT NULL,
			class_letter TEXT NOT NULL,
			class_leader TEXT NOT NULL,
			class_teacher TEXT NOT NULL,
			class_name TEXT GENERATED ALWAYS AS (class_number::text || class_letter) STORED,
			UNIQUE(school_id, class_number, class_letter),
			FOREIGN KEY (school_id) REFERENCES school_profile(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS students (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			class_id INTEGER NOT NULL,
			full_name TEXT NOT NULL,
			birth_year INTEGER,
			gender TEXT,
			address TEXT,
			origin TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (class_id) REFERENCES class_profile(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS lesson_stats (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			class_id INTEGER NOT NULL,
			hour INTEGER NOT NULL,
			subject TEXT NOT NULL,
			students_total INTEGER NOT NULL,
			active_count INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(class_id) REFERENCES class_profile(id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_lesson_unique ON lesson_stats(class_id, hour, subject);`,
		`CREATE TABLE IF NOT EXISTS lesson_active (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			lesson_id INTEGER NOT NULL,
			student_id INTEGER NOT NULL,
			UNIQUE(lesson_id, student_id),
			FOREIGN KEY (lesson_id) REFERENCES lesson_stats(id) ON DELETE CASCADE,
			FOREIGN KEY (student_id) REFERENCES students(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS user_progress (
			user_id INTEGER NOT NULL,
			video_id INTEGER NOT NULL,
			watched_percent INTEGER DEFAULT 0,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, video_id),
			FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE,
			FOREIGN KEY(video_id) REFERENCES news_app_new_video(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS ai_logs (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			user_id INTEGER,
			query TEXT,
			response TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id INTEGER PRIMARY KEY,
			avatar TEXT,
			FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS class_schedule (
			id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			class_id INTEGER NOT NULL,
			day_of_week INTEGER NOT NULL,
			lesson_count INTEGER NOT NULL DEFAULT 0,
			UNIQUE(class_id, day_of_week),
			FOREIGN KEY (class_id) REFERENCES class_profile(id) ON DELETE CASCADE
		);`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}

	// video_id ustunini lesson_stats ga qo'shish (mavjud bo'lmasa)
	db.Exec(`ALTER TABLE lesson_stats ADD COLUMN IF NOT EXISTS video_id INTEGER DEFAULT 0`)

	categories := []string{
		"Ona tili", "Adabiyot", "O'zbek tili", "Qoraqalpoq tili", "Rus tili",
		"Chet tili / Ingliz tili", "Matematika",
		"Informatika va axborot texnologiyalari", "Tabiiy fanlar (Science)",
		"Biologiya", "Fizika", "Kimyo", "Geografiya", "Tarix",
		"O'zbekiston tarixi", "Jahon tarixi", "Tarbiya", "Texnologiya",
		"Tasviriy san'at", "Musiqa madaniyati", "Jismoniy tarbiya",
		"Chaqiruvga qadar boshlang'ich tayyorgarlik", "Astronomiya",
	}
	for _, cat := range categories {
		db.Exec(`INSERT INTO news_app_category (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, cat)
	}

	var adminID int
	err := db.QueryRow(`SELECT id FROM auth_user WHERE is_staff=1 ORDER BY id LIMIT 1`).Scan(&adminID)

	if err == sql.ErrNoRows {
		username := strings.TrimSpace(os.Getenv("ITUBE_ADMIN_USERNAME"))
		if username == "" {
			username = "shuriknurik3"
		}

		password := os.Getenv("ITUBE_ADMIN_PASSWORD")
		if len(password) < 16 {
			return fmt.Errorf("ITUBE_ADMIN_PASSWORD must contain at least 16 characters")
		}

		hashed, hashErr := djangoHashPassword(password, 720000)
		if hashErr != nil {
			return hashErr
		}

		err = db.QueryRow(`
                        INSERT INTO auth_user
                        (username, password, email, region, role, is_staff, is_active)
                        VALUES (?, ?, 'admin@itube.uz', 'Toshkent', 'school', 1, 1)
                        RETURNING id
                `, username, hashed).Scan(&adminID)
	}

	if err != nil {
		return err
	}

	if _, err := db.Exec(`
                INSERT INTO school_profile (user_id, school_name, region)
                VALUES (?, 'Admin maktabi', 'Toshkent')
                ON CONFLICT (user_id) DO NOTHING
        `, adminID); err != nil {
		return err
	}

	if err := ensureLegacyClassAccounts(db); err != nil {
		return err
	}

	return nil
}

// Eski class_teacher loginlari sinf ID loginiga o'zgartiriladi.
// Ma'lumotlar saqlanadi; eski bitta-sinflik hisobning ID si o'zgarmaydi.
func ensureLegacyClassAccounts(db *DB) error {
	rows, err := db.Query(`SELECT id,class_teacher FROM class_profile ORDER BY id`)
	if err != nil {
		return err
	}
	type row struct {
		ID      int
		Teacher string
	}
	var classes []row
	for rows.Next() {
		var c row
		if err := rows.Scan(&c.ID, &c.Teacher); err != nil {
			rows.Close()
			return err
		}
		classes = append(classes, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, c := range classes {
		username := fmt.Sprintf("class_%d", c.ID)
		var existing int
		err = db.QueryRow(`SELECT id FROM auth_user WHERE username=?`, username).Scan(&existing)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		var oldID int
		var storedHash string
		err = db.QueryRow(`SELECT id,password FROM auth_user WHERE username=? AND role='class'`, c.Teacher).Scan(&oldID, &storedHash)
		if err == sql.ErrNoRows {
			log.Printf("Sinf %d uchun yangi parol administrator tomonidan belgilanishi kerak", c.ID)
			continue
		}
		if err != nil {
			return err
		}
		var n int
		if err = db.QueryRow(`SELECT COUNT(*) FROM class_profile WHERE class_teacher=?`, c.Teacher).Scan(&n); err != nil {
			return err
		}
		if n == 1 {
			_, err = db.Exec(`UPDATE auth_user SET username=? WHERE id=? AND role='class'`, username, oldID)
		} else {
			_, err = db.Exec(`INSERT INTO auth_user(username,password,email,region,role,is_staff,is_active)
                VALUES(?,?,'','','class',0,1) ON CONFLICT(username) DO NOTHING`, username, storedHash)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Maktab tanlangach, faqat shu maktab sinflarini ko'rsatamiz.
func (a *App) apiClassesHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("school_id"))
	if err != nil || id < 1 {
		http.Error(w, "Maktab ID noto'g'ri", 400)
		return
	}
	rows, err := a.db.Query(`SELECT id,class_name FROM class_profile WHERE school_id=? ORDER BY class_number,class_letter`, id)
	if err != nil {
		http.Error(w, "Database xatosi", 500)
		return
	}
	defer rows.Close()
	type classOpt struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	results := make([]classOpt, 0)
	for rows.Next() {
		var item classOpt
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			http.Error(w, "Database xatosi", 500)
			return
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "Database xatosi", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(a.accountGuard)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	r.Handle("/media/*", http.StripPrefix("/media/", http.FileServer(http.Dir("media"))))
	r.Get("/admin/manage/schedule", a.adminScheduleHandler)
	r.Post("/admin/manage/schedule/save", a.adminSaveScheduleHandler)

	r.Get("/", a.infoHandler)
	r.Get("/auth", a.authGetHandler)
	r.Get("/auth/", a.authGetHandler)
	r.Post("/auth/login", a.authLoginHandler)
	r.Post("/auth/school-login", a.schoolLoginHandler)
	r.Get("/account/register", a.registerGetHandler)
	r.Get("/account/recover", a.accountRecoverPage)
	r.Post("/account/recover/request", a.accountRecoverRequest)
	r.Post("/account/recover/confirm", a.accountRecoverConfirm)
	r.Post("/account/register", a.authRegisterHandler)
	r.Get("/logout", a.logoutHandler)
	r.Get("/logout/", a.logoutHandler)

	r.Get("/search", a.searchHandler)
	r.Get("/search-api", a.searchAPIHandler)
	r.Get("/video/{slug}", a.newsDetailHandler)

	r.Post("/api/progress", a.progressHandler)
	r.Post("/api/lesson", a.lessonStatHandler)
	r.Post("/api/chat", a.aiChatHandler)
	r.Get("/api/schools", a.apiSchoolsHandler)
	r.Get("/api/classes", a.apiClassesHandler)
	r.Get("/api/class-students", a.apiClassStudentsHandler)

	r.Get("/profile", a.profileHandler)
	r.Post("/profile/avatar", a.uploadAvatarHandler)
	r.Post("/profile/password", a.updatePasswordHandler)
	r.Post("/profile/contact", a.accountContactHandler)
	r.Post("/profile/email/send", a.accountSendVerifyHandler)
	r.Post("/profile/email/verify", a.accountVerifyHandler)
	r.Post("/profile/class", a.updateClassHandler)

	r.Get("/admin/dashboard", a.adminDashboardHandler)
	r.Get("/admin/videos", a.adminVideosHandler)
	r.Get("/admin/videos/", a.adminVideosHandler)
	r.Post("/admin/videos/add", a.adminAddVideoHandler)
	r.Get("/admin/videos/metadata", a.adminVideoMetadataHandler)
	r.Post("/admin/videos/uploader/add", a.adminCreateUploaderHandler)
	r.Post("/admin/videos/uploader/disable/{id}", a.adminDisableUploaderHandler)
	r.Get("/admin/videos/edit/{id}", a.adminEditVideoHandler)
	r.Get("/admin/videos/edit/{id}/", a.adminEditVideoHandler)
	r.Post("/admin/videos/edit/{id}", a.adminUpdateVideoHandler)
	r.Post("/admin/videos/delete/{id}", a.adminDeleteVideoHandler)

	r.Get("/admin/manage", a.adminManageHandler)
	r.Post("/admin/manage/school/add", a.adminAddSchoolHandler)
	r.Post("/admin/manage/class/add", a.adminAddClassHandler)
	r.Post("/admin/manage/student/add", a.adminAddStudentHandler)
	r.Post("/admin/manage/school/delete/{id}", a.adminDeleteSchoolHandler)
	r.Post("/admin/manage/class/delete/{id}", a.adminDeleteClassHandler)
	r.Post("/admin/manage/student/delete/{id}", a.adminDeleteStudentHandler)

	r.Get("/admin/schools", a.adminSchoolsHandler)
	r.Get("/admin/schools/", a.adminSchoolsHandler)
	r.Get("/admin/schools/{id}", a.adminSchoolDetailHandler)
	r.Get("/admin/schools/{id}/", a.adminSchoolDetailHandler)

	r.Get("/school/classes", a.classEditorPage)
	r.Post("/school/classes/{id}/update", a.classEditorUpdate)
	r.Post("/school/classes/{id}/students/add", a.classEditorStudentAdd)
	r.Post("/school/classes/{id}/students/{studentID}/update", a.classEditorStudentUpdate)
	r.Post("/school/classes/{id}/students/{studentID}/delete", a.classEditorStudentDelete)
	r.Get("/myclasses", a.myClassesHandler)
	r.Get("/myclasses/", a.myClassesHandler)

	r.Get("/admin/users", a.adminUsersHandler)
	r.Get("/admin/users/", a.adminUsersHandler)

	return r
}

// ===== RENDER HELPERS =====
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data == nil {
		data = make(map[string]any)
	}
	data = a.addCommonTemplateData(r, data)
	token, _ := a.ensureCSRFToken(w, r)
	data["CSRFToken"] = token
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error for %s: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (a *App) addCommonTemplateData(r *http.Request, data map[string]any) map[string]any {
	if data == nil {
		data = make(map[string]any)
	}
	session, _ := a.store.Get(r, "itube_session")
	if username, ok := session.Values["username"].(string); ok && username != "" {
		data["LoggedIn"] = true
		data["Username"] = username
	}
	if staff, ok := session.Values["is_staff"].(bool); ok {
		data["IsStaff"] = staff
	}
	if role, ok := session.Values["role"].(string); ok {
		data["Role"] = role
	}
	if userID, ok := session.Values["user_id"].(int); ok && userID > 0 {
		var avatar string
		a.db.QueryRow(`SELECT avatar FROM user_profiles WHERE user_id=?`, userID).Scan(&avatar)
		data["Avatar"] = avatar
	}
	return data
}

func (a *App) ensureCSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	session, _ := a.store.Get(r, "itube_session")
	if token, ok := session.Values["csrf_token"].(string); ok && token != "" {
		return token, nil
	}
	token, _ := randomSalt(32)
	session.Values["csrf_token"] = token
	session.Save(r, w)
	return token, nil
}

func (a *App) verifyCSRFToken(r *http.Request) bool {
	session, _ := a.store.Get(r, "itube_session")
	token, ok := session.Values["csrf_token"].(string)
	if !ok {
		return false
	}
	return r.PostFormValue("csrf_token") == token
}

func (a *App) currentUserID(r *http.Request) (int, bool) {
	session, _ := a.store.Get(r, "itube_session")
	id, ok := session.Values["user_id"].(int)
	return id, ok && id > 0
}
func (a *App) currentUserIsStaff(r *http.Request) bool {
	session, _ := a.store.Get(r, "itube_session")
	staff, _ := session.Values["is_staff"].(bool)
	return staff
}
func (a *App) currentUserRole(r *http.Request) string {
	session, _ := a.store.Get(r, "itube_session")
	role, _ := session.Values["role"].(string)
	return role
}

// ===== ADMIN SCHEDULE =====
func (a *App) adminScheduleHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	schoolIDStr := r.URL.Query().Get("school_id")
	classIDStr := r.URL.Query().Get("class_id")
	schoolID, _ := strconv.Atoi(schoolIDStr)
	classID, _ := strconv.Atoi(classIDStr)

	if schoolID == 0 || classID == 0 {
		http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
		return
	}

	var school SchoolProfile
	a.db.QueryRow(`SELECT id, user_id, school_name, region FROM school_profile WHERE id=?`, schoolID).
		Scan(&school.ID, &school.UserID, &school.SchoolName, &school.Region)

	var class ClassProfile
	a.db.QueryRow(`SELECT id, class_number, class_letter, class_leader, class_teacher, class_name FROM class_profile WHERE id=?`, classID).
		Scan(&class.ID, &class.ClassNumber, &class.ClassLetter, &class.ClassLeader, &class.ClassTeacher, &class.ClassName)

	// Mavjud jadval
	schedule := map[int]int{}
	rows, _ := a.db.Query(`SELECT day_of_week, lesson_count FROM class_schedule WHERE class_id=?`, classID)
	if rows != nil {
		for rows.Next() {
			var day, cnt int
			rows.Scan(&day, &cnt)
			schedule[day] = cnt
		}
		rows.Close()
	}

	days := []struct {
		Num  int
		Name string
	}{
		{1, "Dushanba"},
		{2, "Seshanba"},
		{3, "Chorshanba"},
		{4, "Payshanba"},
		{5, "Juma"},
		{6, "Shanba"},
	}
	type DayItem struct {
		Num   int
		Name  string
		Count int
	}
	var dayItems []DayItem
	for _, d := range days {
		dayItems = append(dayItems, DayItem{Num: d.Num, Name: d.Name, Count: schedule[d.Num]})
	}

	a.render(w, r, "admin_schedule.html", map[string]any{
		"School": school,
		"Class":  class,
		"Days":   dayItems,
	})
}

func (a *App) adminSaveScheduleHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	schoolIDStr := r.FormValue("school_id")
	classIDStr := r.FormValue("class_id")
	schoolID, _ := strconv.Atoi(schoolIDStr)
	classID, _ := strconv.Atoi(classIDStr)
	if schoolID == 0 || classID == 0 {
		http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
		return
	}

	for day := 1; day <= 6; day++ {
		fieldName := "day_" + strconv.Itoa(day)
		cntStr := strings.TrimSpace(r.FormValue(fieldName))
		cnt, _ := strconv.Atoi(cntStr)
		if cnt < 0 {
			cnt = 0
		}
		// UPSERT
		var exists int
		a.db.QueryRow(`SELECT COUNT(*) FROM class_schedule WHERE class_id=? AND day_of_week=?`, classID, day).Scan(&exists)
		if exists > 0 {
			a.db.Exec(`UPDATE class_schedule SET lesson_count=? WHERE class_id=? AND day_of_week=?`, cnt, classID, day)
		} else {
			a.db.Exec(`INSERT INTO class_schedule (class_id, day_of_week, lesson_count) VALUES (?, ?, ?)`, classID, day, cnt)
		}
	}

	http.Redirect(w, r, "/admin/manage?school_id="+schoolIDStr+"&class_id="+classIDStr, http.StatusSeeOther)
}

// ===== AUTH HANDLERS =====
func (a *App) infoHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); ok {
		news, _ := a.latestPublishedVideos(50)
		cats, _ := a.allCategories()
		a.render(w, r, "index.html", map[string]any{
			"Videos":      news,
			"Categories":  cats,
			"ResultCount": len(news),
		})
		return
	}
	http.Redirect(w, r, "/auth", http.StatusSeeOther)
}

func (a *App) authGetHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "auth.html", nil)
}

func (a *App) registerGetHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "register.html", nil)
}

func (a *App) authRegisterHandler(w http.ResponseWriter, r *http.Request) {
    if err:=r.ParseForm();err!=nil {http.Error(w,"bad form",400);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    username:=strings.TrimSpace(r.FormValue("username"))
    password:=r.FormValue("password")
    email,validEmail:=accountEmail(r.FormValue("email"))
    region,validRegion:=accountRegion(r.FormValue("region"))
    if len([]rune(username))<3 || len([]rune(username))>80 || len([]rune(password))<12 || len(password)>256 || !validEmail || !validRegion {
        a.render(w,r,"register.html",map[string]any{"Error":"Username 3+ belgi, parol 12+ belgi, email va viloyat talab qilinadi."});return
    }
    hash,err:=djangoHashPassword(password,720000)
    if err!=nil {http.Error(w,"password error",500);return}
    var id int
    err=a.db.QueryRow(`INSERT INTO auth_user(username,password,email,region,role,is_staff,is_active,email_verified)
      VALUES(?,?,?,?,'user',0,1,FALSE) RETURNING id`,username,hash,email,region).Scan(&id)
    if err!=nil {a.render(w,r,"register.html",map[string]any{"Error":"Username band yoki ma'lumotlarni saqlashda xatolik."});return}
    session,_:=a.store.Get(r,"itube_session")
    session.Values["user_id"]=id
    session.Values["username"]=username
    session.Values["is_staff"]=false
    session.Values["role"]="user"
    session.Values["account_version"]=1
    if err=session.Save(r,w);err!=nil {http.Error(w,"session",500);return}
    http.Redirect(w,r,"/profile?notice=need-verify",303)
}

func (a *App) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		a.render(w, r, "auth.html", map[string]any{"Error": "Username va parolni kiriting."})
		return
	}
	var user User
	var isStaff int
	var role string
	err := a.db.QueryRow(`
		SELECT id, username, password, email, region, role, is_staff, is_active
		FROM auth_user WHERE username=? AND is_active=1
	`, username).Scan(&user.ID, &user.Username, &user.Password, &user.Email, &user.Region, &role, &isStaff, &user.IsActive)
	if err != nil || role == "class" || !djangoCheckPassword(user.Password, password) {
		a.render(w, r, "auth.html", map[string]any{"Error": "NotoР Р†Р вЂљР’ВgР Р†Р вЂљР’Вri username yoki parol."})
		return
	}
	session, _ := a.store.Get(r, "itube_session")
	session.Values["user_id"] = user.ID
	session.Values["username"] = user.Username
	session.Values["is_staff"] = isStaff == 1
	session.Values["role"] = role
    var accountVersion int
    a.stampAccountSession(user.ID,&accountVersion)
    session.Values["account_version"] = accountVersion
	session.Save(r, w)
	if isStaff == 1 {
		http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
	} else if role == "uploader" {
		http.Redirect(w, r, "/admin/videos", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (a *App) schoolLoginHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Forma xatosi", 400)
		return
	}
	if !a.verifyCSRFToken(r) {
		http.Error(w, "CSRF xatosi", 400)
		return
	}
	schoolID, e := strconv.Atoi(strings.TrimSpace(r.FormValue("school_id")))
	classID, ce := strconv.Atoi(strings.TrimSpace(r.FormValue("class_id")))
	password := r.FormValue("password")
	if e != nil || schoolID < 1 || password == "" {
		a.render(w, r, "auth.html", map[string]any{"Error": "Maktabni tanlang va parol kiriting."})
		return
	}
	var userID int
	var username, hash, role string
	if r.FormValue("class_id") != "" { // Sinf uchun tanlangan sinfning hisobini tekshiramiz.
		if ce != nil || classID < 1 {
			a.render(w, r, "auth.html", map[string]any{"Error": "Sinf noto'g'ri tanlangan."})
			return
		}
		err := a.db.QueryRow(`
            SELECT u.id, u.username, u.password, u.role
            FROM class_profile c JOIN auth_user u ON u.username=('class_' || c.id::text)
            WHERE c.id=? AND c.school_id=? AND u.role='class'
              AND u.is_staff=0 AND u.is_active=1
        `, classID, schoolID).Scan(&userID, &username, &hash, &role)
		if err != nil || !djangoCheckPassword(hash, password) {
			a.render(w, r, "auth.html", map[string]any{"Error": "Sinf paroli noto'g'ri."})
			return
		}
	} else { // Sinf tanlanmagan bo'lsa, maktab hisobiga kiriladi.
		err := a.db.QueryRow(`
            SELECT u.id, u.username, u.password, u.role
            FROM school_profile s JOIN auth_user u ON u.id=s.user_id
            WHERE s.id=? AND u.role='school' AND u.is_staff=0 AND u.is_active=1
        `, schoolID).Scan(&userID, &username, &hash, &role)
		if err != nil || !djangoCheckPassword(hash, password) {
			a.render(w, r, "auth.html", map[string]any{"Error": "Maktab paroli noto'g'ri."})
			return
		}
	}
	session, err := a.store.Get(r, "itube_session")
	if err != nil {
		http.Error(w, "Sessiya xatosi", 500)
		return
	}
	session.Values["user_id"] = userID
	session.Values["username"] = username
	session.Values["role"] = role
    var accountVersion int
    a.stampAccountSession(userID,&accountVersion)
    session.Values["account_version"] = accountVersion
	session.Values["is_staff"] = false
	delete(session.Values, "class_id")
	if role == "class" {
		session.Values["class_id"] = classID
	}
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Sessiyani saqlashda xato", 500)
		return
	}
	if role == "class" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/myclasses", http.StatusSeeOther)
	}
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := a.store.Get(r, "itube_session")
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/auth", http.StatusSeeOther)
}

// ===== API =====
func (a *App) apiSchoolsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`
		SELECT sp.id, sp.school_name, sp.region
		FROM school_profile sp
		JOIN auth_user u ON u.id = sp.user_id
		WHERE u.is_active = 1
		ORDER BY sp.school_name
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type School struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	var schools []School
	for rows.Next() {
		var s School
		if err := rows.Scan(&s.ID, &s.Name, &s.Region); err != nil {
			continue
		}
		schools = append(schools, s)
	}
	writeJSON(w, map[string]any{"results": schools})
}

func (a *App) apiClassStudentsHandler(w http.ResponseWriter, r *http.Request) {
	// API_CLASS_STUDENTS_ACCESS_CHECK
	if !a.currentUserIsStaff(r) {
		userID, loggedIn := a.currentUserID(r)
		if !loggedIn {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		classID, err := strconv.Atoi(r.URL.Query().Get("class_id"))
		if err != nil || classID <= 0 {
			http.Error(w, "Invalid class ID", http.StatusBadRequest)
			return
		}

		allowed := false

		switch a.currentUserRole(r) {
		case "school":
			var found int
			err := a.db.QueryRow(`
                SELECT 1 FROM class_profile c
                JOIN school_profile s ON s.id = c.school_id
                JOIN auth_user u ON u.id = s.user_id
                WHERE c.id=? AND s.user_id=? AND u.is_active=1
            `, classID, userID).Scan(&found)
			allowed = err == nil

		case "class":
			session, err := a.store.Get(r, "itube_session")
			if err == nil {
				sessionClassID, ok := session.Values["class_id"].(int)
				if ok && sessionClassID == classID {
					var found int
					err := a.db.QueryRow(`
                        SELECT 1 FROM class_profile c
                        JOIN auth_user u ON u.username=('class_' || c.id::text) AND u.role='class'
                        WHERE c.id=? AND u.id=? AND u.is_active=1
                    `, classID, userID).Scan(&found)
					allowed = err == nil
				}
			}
		}

		if !allowed {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}
	classIDStr := r.URL.Query().Get("class_id")
	classID, err := strconv.Atoi(classIDStr)
	if err != nil || classID == 0 {
		writeJSON(w, map[string]any{"results": []any{}})
		return
	}
	rows, err := a.db.Query(`SELECT id, full_name FROM students WHERE class_id=? ORDER BY full_name`, classID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type Student struct {
		ID       int    `json:"id"`
		FullName string `json:"full_name"`
	}
	var students []Student
	for rows.Next() {
		var s Student
		rows.Scan(&s.ID, &s.FullName)
		students = append(students, s)
	}
	writeJSON(w, map[string]any{"results": students})
}

// ===== VIDEO =====
func (a *App) latestPublishedVideos(limit int) ([]Video, error) {
	rows, err := a.db.Query(`
		SELECT v.id, v.title, v.slug, v.body, v.video_url, v.image, v.status, v.created_time, c.id, c.name
		FROM news_app_new_video v JOIN news_app_category c ON c.id=v.category_id
		WHERE v.status='PB' ORDER BY v.created_time DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videos []Video
	for rows.Next() {
		var v Video
		var img, url sql.NullString
		rows.Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName)
		v.VideoURL = url.String
		v.Image = img.String
		videos = append(videos, v)
	}
	return videos, nil
}
func (a *App) allCategories() ([]Category, error) {
	rows, err := a.db.Query(`SELECT id, name FROM news_app_category ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []Category
	for rows.Next() {
		var c Category
		rows.Scan(&c.ID, &c.Name)
		cats = append(cats, c)
	}
	return cats, nil
}
func (a *App) searchPublishedVideos(query string, limit int) ([]Video, error) {
	like := "%" + query + "%"
	rows, err := a.db.Query(`
		SELECT v.id, v.title, v.slug, v.body, v.video_url, v.image, v.status, v.created_time, c.id, c.name
		FROM news_app_new_video v JOIN news_app_category c ON c.id=v.category_id
		WHERE v.status='PB' AND (v.title ILIKE ? OR v.body ILIKE ? OR c.name ILIKE ?)
		ORDER BY v.created_time DESC LIMIT ?
	`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videos []Video
	for rows.Next() {
		var v Video
		var img, url sql.NullString
		rows.Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName)
		v.VideoURL = url.String
		v.Image = img.String
		videos = append(videos, v)
	}
	return videos, nil
}
func (a *App) videoBySlug(slug string) (Video, error) {
	var v Video
	var img, url sql.NullString
	err := a.db.QueryRow(`
		SELECT v.id, v.title, v.slug, v.body, v.video_url, v.image, v.status, v.created_time, c.id, c.name
		FROM news_app_new_video v JOIN news_app_category c ON c.id=v.category_id
		WHERE v.slug=?
	`, slug).Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName)
	if err != nil {
		return Video{}, err
	}
	v.VideoURL = url.String
	v.Image = img.String
	return v, nil
}
func (a *App) videoByID(id int) (Video, error) {
	var v Video
	var img, url sql.NullString
	err := a.db.QueryRow(`
		SELECT v.id, v.title, v.slug, v.body, v.video_url, v.image, v.status, v.created_time, c.id, c.name
		FROM news_app_new_video v JOIN news_app_category c ON c.id=v.category_id
		WHERE v.id=?
	`, id).Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName)
	if err != nil {
		return Video{}, err
	}
	v.VideoURL = url.String
	v.Image = img.String
	return v, nil
}

func (a *App) searchHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	q := r.URL.Query().Get("q")
	results, _ := a.searchPublishedVideos(q, 20)
	cats, _ := a.allCategories()
	a.render(w, r, "search_results.html", map[string]any{"News": results, "Query": q, "Categories": cats})
}
func (a *App) searchAPIHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); !ok {
		writeJSON(w, map[string]any{"error": "unauthorized"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, map[string]any{"results": []any{}})
		return
	}
	results, _ := a.searchPublishedVideos(q, 20)
	type item struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		Slug     string `json:"slug"`
		Category string `json:"category"`
		Body     string `json:"body"`
		Image    string `json:"image"`
	}
	resp := make([]item, len(results))
	for i, v := range results {
		body := v.Body
		if len(body) > 100 {
			body = body[:100] + "..."
		}
		img := v.Image
		if img == "" && isYouTube(v.VideoURL) {
			img = youtubeThumb(v.VideoURL)
		}
		resp[i] = item{ID: v.ID, Title: v.Title, Slug: v.Slug, Category: v.CategoryName, Body: body, Image: img}
	}
	writeJSON(w, map[string]any{"results": resp})
}

func (a *App) newsDetailHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	slug := chi.URLParam(r, "slug")
	video, err := a.videoBySlug(slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	userID, _ := a.currentUserID(r)
	var progress int
	a.db.QueryRow(`SELECT watched_percent FROM user_progress WHERE user_id=? AND video_id=?`, userID, video.ID).Scan(&progress)

	role := a.currentUserRole(r)
	var classID int
	var classStudents []Student
	if role == "class" {
		session, _ := a.store.Get(r, "itube_session")
		if cid, ok := session.Values["class_id"].(int); ok && cid > 0 {
			classID = cid
			rows, err := a.db.Query(`SELECT id, full_name, COALESCE(birth_year,0), COALESCE(gender,''), COALESCE(address,''), COALESCE(origin,'') FROM students WHERE class_id=? ORDER BY full_name`, classID)
			if err == nil {
				for rows.Next() {
					var s Student
					rows.Scan(&s.ID, &s.FullName, &s.BirthYear, &s.Gender, &s.Address, &s.Origin)
					s.ClassID = classID
					classStudents = append(classStudents, s)
				}
				rows.Close()
			}
		}
	}

	a.render(w, r, "single_page.html", map[string]any{
		"New":           video,
		"Progress":      progress,
		"Role":          role,
		"ClassID":       classID,
		"ClassStudents": classStudents,
	})
}

// ===== PROGRESS =====
func (a *App) progressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	userID, ok := a.currentUserID(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var payload struct {
		VideoID int `json:"video_id"`
		Percent int `json:"percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if payload.VideoID == 0 {
		http.Error(w, "video id required", 400)
		return
	}
	var current int
	a.db.QueryRow(`SELECT watched_percent FROM user_progress WHERE user_id=? AND video_id=?`, userID, payload.VideoID).Scan(&current)
	if payload.Percent <= current {
		writeJSON(w, map[string]any{"status": "ok"})
		return
	}
	_, err := a.db.Exec(`
		INSERT INTO user_progress (user_id, video_id, watched_percent, updated_at)
		VALUES (?, ?, ?, NOW())
		ON CONFLICT(user_id, video_id) DO UPDATE SET
			watched_percent = excluded.watched_percent,
			updated_at = NOW()
	`, userID, payload.VideoID, payload.Percent)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

// ===== LESSON STAT =====
func (a *App) lessonStatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := a.currentUserID(r); !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	role := a.currentUserRole(r)
	if role != "class" {
		http.Error(w, "faqat sinf vakillari uchun", 403)
		return
	}
	var payload struct {
		Hour      int    `json:"hour"`
		Subject   string `json:"subject"`
		VideoID   int    `json:"video_id"`
		ActiveIDs []int  `json:"active_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if payload.Hour < 1 || payload.Subject == "" {
		http.Error(w, "barcha maydonlarni to'ldiring", 400)
		return
	}
	session, _ := a.store.Get(r, "itube_session")
	classID, ok := session.Values["class_id"].(int)
	if !ok || classID == 0 {
		http.Error(w, "sinf topilmadi", 404)
		return
	}

	var totalStudents int
	a.db.QueryRow(`SELECT COUNT(*) FROM students WHERE class_id=?`, classID).Scan(&totalStudents)

	activeCount := len(payload.ActiveIDs)

	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM lesson_stats WHERE class_id=? AND hour=? AND subject=?`,
		classID, payload.Hour, payload.Subject).Scan(&count)

	var lessonID int
	if count > 0 {
		a.db.QueryRow(`SELECT id FROM lesson_stats WHERE class_id=? AND hour=? AND subject=?`,
			classID, payload.Hour, payload.Subject).Scan(&lessonID)
		a.db.Exec(`
			UPDATE lesson_stats SET students_total=?, active_count=?, video_id=?, created_at=NOW()
			WHERE id=?
		`, totalStudents, activeCount, payload.VideoID, lessonID)
		a.db.Exec(`DELETE FROM lesson_active WHERE lesson_id=?`, lessonID)
	} else {
		insertErr := a.db.QueryRow(`
INSERT INTO lesson_stats (class_id, hour, subject, video_id, students_total, active_count, created_at)
VALUES (?, ?, ?, ?, ?, ?, NOW())
RETURNING id
`, classID, payload.Hour, payload.Subject, payload.VideoID, totalStudents, activeCount).Scan(&lessonID)
		if insertErr != nil {
			http.Error(w, insertErr.Error(), 500)
			return
		}
	}

	for _, sid := range payload.ActiveIDs {
		a.db.Exec(`INSERT INTO lesson_active (lesson_id, student_id) VALUES (?, ?) ON CONFLICT (lesson_id, student_id) DO NOTHING`, lessonID, sid)
	}

	writeJSON(w, map[string]any{"status": "ok", "active_count": activeCount, "total": totalStudents})
}

// ===== AI CHAT =====
func (a *App) aiChatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	userID, _ := a.currentUserID(r)
	if userID == 0 {
		http.Error(w, "unauthorized", 401)
		return
	}
	var payload struct {
		Query   string `json:"query"`
		VideoID int    `json:"video_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	query := strings.TrimSpace(payload.Query)
	if query == "" {
		writeJSON(w, map[string]any{"response": "Savol yozing."})
		return
	}
	var context string
	if payload.VideoID > 0 {
		video, err := a.videoByID(payload.VideoID)
		if err == nil {
			context = fmt.Sprintf("Video sarlavhasi: %s\nVideo tavsifi: %s", video.Title, video.Body)
		}
	}
	finalQuery := query
	if context != "" {
		finalQuery = context + "\n\nFoydalanuvchi savoli: " + query
	}
	resp, err := callOpenRouterAPI(finalQuery)
	if err != nil {
		http.Error(w, "AI xatosi: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"response": resp})
	a.db.Exec(`INSERT INTO ai_logs (user_id, query, response, created_at) VALUES (?, ?, ?, NOW())`, userID, finalQuery, resp)
}

// ===== PROFILE =====
func (a *App) profileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.currentUserID(r)
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	var user User
	var role string
	err := a.db.QueryRow(`SELECT id, username, email, region, role, is_staff FROM auth_user WHERE id=?`, userID).Scan(&user.ID, &user.Username, &user.Email, &user.Region, &role, &user.IsStaff)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var avatar string
	a.db.QueryRow(`SELECT avatar FROM user_profiles WHERE user_id=?`, userID).Scan(&avatar)
	schoolOrClass := ""
	classNum, classLetter, classLeader := 0, "", ""
	if role == "school" {
		a.db.QueryRow(`SELECT school_name FROM school_profile WHERE user_id=?`, userID).Scan(&schoolOrClass)
	} else if role == "class" {
		var className string
		a.db.QueryRow(`
			SELECT cp.class_name, cp.class_number, cp.class_letter, cp.class_leader
			FROM class_profile cp
			JOIN school_profile sp ON sp.id = cp.school_id
			JOIN auth_user u ON u.id = sp.user_id
			WHERE ('class_' || cp.id::text) = (SELECT username FROM auth_user WHERE id=? AND role='class')
		`, userID).Scan(&className, &classNum, &classLetter, &classLeader)
		schoolOrClass = className
	}
	var emailVerified bool
    _=a.db.QueryRow(`SELECT email_verified FROM auth_user WHERE id=?`,userID).Scan(&emailVerified)
    notice:=""
    switch r.URL.Query().Get("notice") {
    case "saved":notice="Ma'lumot saqlandi. Emailni tasdiqlang."
    case "verified":notice="Email tasdiqlandi."
    case "sent":notice="Tasdiqlash kodi emailga yuborildi."
    case "wrong-code":notice="Kod noto'g'ri yoki muddati tugagan."
    case "need-email","need-verify":notice="Parolni tiklash uchun emailingizni tasdiqlang."
    case "mail-error":notice="Xat yuborilmadi. SMTP sozlamalarini tekshiring yoki keyin qayta urinib ko'ring."
    }
    a.render(w, r, "profile.html", map[string]any{
        "Email":user.Email,"Region":user.Region,"EmailVerified":emailVerified,"Notice":notice,

		"Username": user.Username, "Avatar": avatar,
		"SchoolOrClass": schoolOrClass, "Role": role,
		"ClassNumber": classNum, "ClassLetter": classLetter, "ClassLeader": classLeader,
	})
}

func (a *App) uploadAvatarHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	userID, ok := a.currentUserID(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		writeJSON(w, map[string]any{"status": "error", "error": "Rasm yuklanmadi"})
		return
	}
	defer file.Close()
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".png"
	}
	filename := fmt.Sprintf("avatar_%d_%d%s", userID, time.Now().Unix(), ext)
	path := filepath.Join("static", "avatars", filename)
	os.MkdirAll(filepath.Dir(path), 0755)
	dst, err := os.Create(path)
	if err != nil {
		writeJSON(w, map[string]any{"status": "error", "error": "Fayl yozishda xatolik"})
		return
	}
	defer dst.Close()
	io.Copy(dst, file)
	avatarPath := "/static/avatars/" + filename
	a.db.Exec(`INSERT INTO user_profiles (user_id, avatar) VALUES (?, ?) ON CONFLICT (user_id) DO UPDATE SET avatar = EXCLUDED.avatar`, userID, avatarPath)
	writeJSON(w, map[string]any{"status": "ok", "avatar": avatarPath})
}

func (a *App) updatePasswordHandler(w http.ResponseWriter, r *http.Request) {
    userID,ok:=a.currentUserID(r);if !ok {http.Error(w,"unauthorized",401);return}
    session,_:=a.store.Get(r,"itube_session")
    expected,_:=session.Values["csrf_token"].(string)
    if expected=="" || r.Header.Get("X-CSRF-Token")!=expected {http.Error(w,"csrf",403);return}
    var payload struct { Current string `json:"current"`; New string `json:"new"` }
    if err:=json.NewDecoder(http.MaxBytesReader(w,r.Body,8192)).Decode(&payload);err!=nil {http.Error(w,"json",400);return}
    if len([]rune(payload.New))<12 || len(payload.New)>256 || (a.currentUserIsStaff(r) && len([]rune(payload.New))<16) {
        writeJSON(w,map[string]any{"status":"error","error":"Parol kamida 12 belgi, admin uchun 16 belgi bo'lsin."});return
    }
    var oldHash string
    if err:=a.db.QueryRow(`SELECT password FROM auth_user WHERE id=?`,userID).Scan(&oldHash);err!=nil || !djangoCheckPassword(oldHash,payload.Current) {
        writeJSON(w,map[string]any{"status":"error","error":"Joriy parol noto'g'ri"});return
    }
    newHash,err:=djangoHashPassword(payload.New,720000);if err!=nil {http.Error(w,"hash",500);return}
    if _,err=a.db.Exec(`UPDATE auth_user SET password=?,session_version=session_version+1 WHERE id=?`,newHash,userID);err!=nil {http.Error(w,"db",500);return}
    var ver int
    a.stampAccountSession(userID,&ver)
    session.Values["account_version"]=ver
    _=session.Save(r,w)
    writeJSON(w,map[string]any{"status":"ok"})
}

func (a *App) updateClassHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
    session,_:=a.store.Get(r,"itube_session")
    csrf,_:=session.Values["csrf_token"].(string)
    if csrf=="" || r.Header.Get("X-CSRF-Token")!=csrf {http.Error(w,"csrf",403);return}
    if !a.currentUserIsStaff(r) && a.currentUserRole(r)!="school" {http.Error(w,"forbidden",403);return}
	userID, ok := a.currentUserID(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var payload struct {
		ClassNumber int    `json:"class_number"`
		ClassLetter string `json:"class_letter"`
		ClassLeader string `json:"class_leader"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	_, err := a.db.Exec(`
		UPDATE class_profile
		SET class_number=?, class_letter=?, class_leader=?
		WHERE ('class_' || id::text) = (SELECT username FROM auth_user WHERE id=? AND role='class')
	`, payload.ClassNumber, payload.ClassLetter, payload.ClassLeader, userID)
	if err != nil {
		writeJSON(w, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

// adminDashboardHandler renders database-backed overview for authorized staff.
func (a *App) adminDashboardHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var total, published, schools, classes, students, categories, users int
	for _, metric := range []struct {
		dest  *int
		query string
	}{
		{&total, `SELECT COUNT(*) FROM news_app_new_video`},
		{&published, `SELECT COUNT(*) FROM news_app_new_video WHERE status='PB'`},
		{&schools, `SELECT COUNT(*) FROM school_profile`},
		{&classes, `SELECT COUNT(*) FROM class_profile`},
		{&students, `SELECT COUNT(*) FROM students`},
		{&categories, `SELECT COUNT(*) FROM news_app_category`},
		{&users, `SELECT COUNT(*) FROM auth_user WHERE role='user' AND is_staff=0`},
	} {
		if err := a.db.QueryRow(metric.query).Scan(metric.dest); err != nil {
			log.Printf("dashboard metric query failed: %v", err)
			http.Error(w, "Statistikani yuklashda xatolik", http.StatusInternalServerError)
			return
		}
	}
	type recentVideo struct {
		ID      int
		Title   string
		Status  string
		Created string
	}
	var recent []recentVideo
	rows, err := a.db.Query(`
		SELECT id, COALESCE(title,''), COALESCE(status,'DR'),
		       COALESCE(TO_CHAR(created_time, 'DD.MM.YYYY'),'—')
		FROM news_app_new_video
		ORDER BY created_time DESC NULLS LAST, id DESC LIMIT 6
	`)
	if err != nil {
		log.Printf("dashboard recent videos query failed: %v", err)
		http.Error(w, "Videolarni yuklashda xatolik", http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var video recentVideo
		if err := rows.Scan(&video.ID, &video.Title, &video.Status, &video.Created); err != nil {
			rows.Close()
			log.Printf("dashboard recent video scan failed: %v", err)
			http.Error(w, "Videolarni o‘qishda xatolik", http.StatusInternalServerError)
			return
		}
		recent = append(recent, video)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("dashboard recent videos iterator failed: %v", err)
		http.Error(w, "Videolarni o‘qishda xatolik", http.StatusInternalServerError)
		return
	}
	rows.Close()
	publicationPercent := 0
	if total > 0 {
		publicationPercent = published * 100 / total
	}
	a.render(w, r, "admin_dashboard.html", map[string]any{
		"TotalVideos": total, "PublishedCount": published, "DraftCount": total - published,
		"SchoolCount": schools, "ClassCount": classes, "StudentCount": students,
		"CategoryCount": categories, "UserCount": users,
		"PublicationPercent": publicationPercent, "RecentVideos": recent,
	})
}

// ===== ADMIN VIDEOS =====
// Make safe, stable, unique URLs without breaking Uzbek or Russian title letters.
func itubeVideoSlug(title string, id int64) string {
	var b strings.Builder
	lastDash := false
	count := 0
	for _, ch := range strings.ToLower(title) {
		if count >= 75 {
			break
		}
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			b.WriteRune(ch)
			lastDash = false
			count++
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "video"
	}
	return fmt.Sprintf("%s-%d", base, id)
}

// Video uploaders can create and publish new videos, but cannot edit or delete.
func (a *App) canSubmitVideo(r *http.Request) bool {
	if a.currentUserIsStaff(r) {
		return true
	}
	if a.currentUserRole(r) != "uploader" {
		return false
	}
	id, loggedIn := a.currentUserID(r)
	if !loggedIn {
		return false
	}
	var active int
	err := a.db.QueryRow(`SELECT is_active FROM auth_user WHERE id=? AND role='uploader'`, id).Scan(&active)
	return err == nil && active == 1
}

func (a *App) adminVideosHandler(w http.ResponseWriter, r *http.Request) {
	if !a.canSubmitVideo(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	videos, err := a.allVideos()
	if err != nil {
		log.Printf("load videos: %v", err)
		http.Error(w, "Videolarni yuklab bo'lmadi", 500)
		return
	}
	cats, err := a.allCategories()
	if err != nil {
		log.Printf("load categories: %v", err)
		http.Error(w, "Fanlarni yuklab bo'lmadi", 500)
		return
	}
	type uploaderRow struct {
		ID       int
		Username string
		Active   bool
	}
	uploaders := make([]uploaderRow, 0)
	if a.currentUserIsStaff(r) {
		rows, err := a.db.Query(`SELECT id, username, is_active FROM auth_user WHERE role='uploader' ORDER BY id DESC`)
		if err != nil {
			http.Error(w, "Xodimlar ro'yxati topilmadi", 500)
			return
		}
		for rows.Next() {
			var item uploaderRow
			var active int
			if err := rows.Scan(&item.ID, &item.Username, &active); err != nil {
				rows.Close()
				http.Error(w, "Xodimlar ro'yxatida xato", 500)
				return
			}
			item.Active = active == 1
			uploaders = append(uploaders, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			http.Error(w, "Xodimlar ro'yxatida xato", 500)
			return
		}
	}
	pub := 0
	for _, v := range videos {
		if v.Status == "PB" {
			pub++
		}
	}
	a.render(w, r, "admin_videos.html", map[string]any{
		"Videos": videos, "Categories": cats, "TotalVideos": len(videos),
		"PublishedCount": pub, "DraftCount": len(videos) - pub,
		"Uploaders":       uploaders,
		"Added":           r.URL.Query().Get("added") == "1",
		"UploaderCreated": r.URL.Query().Get("worker") == "ok" && a.currentUserIsStaff(r),
	})
}

// Fetched only from a fixed YouTube endpoint, so user-provided URLs cannot trigger SSRF.
func (a *App) adminVideoMetadataHandler(w http.ResponseWriter, r *http.Request) {
	if !a.canSubmitVideo(r) {
		http.Error(w, "Ruxsat yo'q", http.StatusForbidden)
		return
	}
	id, ok := itubeYouTubeID(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "YouTube video havolasini kiriting", http.StatusBadRequest)
		return
	}
	videoURL := "https://www.youtube.com/watch?v=" + id
	metadataURL := "https://www.youtube.com/oembed?url=" + url.QueryEscape(videoURL) + "&format=json"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, metadataURL, nil)
	if err != nil {
		http.Error(w, "Havola xatosi", 500)
		return
	}
	client := &http.Client{Timeout: 7 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "YouTube sarlavhasini olib bo'lmadi", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Video ma'lumoti topilmadi, qo'lda kiriting", http.StatusBadGateway)
		return
	}
	var meta struct {
		Title  string `json:"title"`
		Author string `json:"author_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128*1024)).Decode(&meta); err != nil || strings.TrimSpace(meta.Title) == "" {
		http.Error(w, "Sarlavhani qo'lda kiriting", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{
		"title": meta.Title, "author": meta.Author, "video_url": videoURL,
		"thumbnail": "https://img.youtube.com/vi/" + id + "/hqdefault.jpg",
	})
}

// Admin creates a least-privilege account for a content employee.
func (a *App) adminCreateUploaderHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Error(w, "Ruxsat yo'q", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil || !a.verifyCSRFToken(r) {
		http.Error(w, "Forma xavfsizlik xatosi", 400)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if len(username) < 3 || len(username) > 32 || len(password) < 14 || len(password) > 256 {
		http.Error(w, "Login 3-32, parol kamida 14 belgi bo'lishi kerak", 400)
		return
	}
	for _, ch := range username {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.') {
			http.Error(w, "Login uchun faqat lotin harf, raqam, _ . - ishlating", 400)
			return
		}
	}
	hash, err := djangoHashPassword(password, 720000)
	if err != nil {
		http.Error(w, "Parol xatosi", 500)
		return
	}
	_, err = a.db.Exec(`INSERT INTO auth_user(username,password,email,region,role,is_staff,is_active)
        VALUES (?,?,'','','uploader',0,1)`, username, hash)
	if err != nil {
		log.Printf("create uploader failed: %v", err)
		http.Error(w, "Login band yoki bazada xato", 409)
		return
	}
	http.Redirect(w, r, "/admin/videos?worker=ok", http.StatusSeeOther)
}

func (a *App) adminDisableUploaderHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Error(w, "Ruxsat yo'q", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil || !a.verifyCSRFToken(r) {
		http.Error(w, "CSRF xatosi", 400)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id < 1 {
		http.Error(w, "Xodim topilmadi", 400)
		return
	}
	res, err := a.db.Exec(`UPDATE auth_user SET is_active=0 WHERE id=? AND role='uploader' AND is_staff=0`, id)
	if err != nil {
		http.Error(w, "Xodim hisobi to'xtatilmadi", 500)
		return
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		http.Error(w, "Xodim topilmadi", 404)
		return
	}
	http.Redirect(w, r, "/admin/videos?worker=disabled", http.StatusSeeOther)
}

func (a *App) allVideos() ([]Video, error) {
	rows, err := a.db.Query(`
		SELECT v.id, v.title, v.slug, v.body, v.video_url, v.image, v.status, v.created_time, c.id, c.name
		FROM news_app_new_video v JOIN news_app_category c ON c.id=v.category_id
		ORDER BY v.created_time DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videos []Video
	for rows.Next() {
		var v Video
		var img, url sql.NullString
		if err := rows.Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName); err != nil {
			return nil, err
		}
		v.VideoURL = url.String
		v.Image = img.String
		videos = append(videos, v)
	}
	return videos, rows.Err()
}
func (a *App) adminAddVideoHandler(w http.ResponseWriter, r *http.Request) {
	if !a.canSubmitVideo(r) {
		http.Error(w, "Ruxsat yo'q", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil || !a.verifyCSRFToken(r) {
		http.Error(w, "Forma xavfsizlik xatosi", 400)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	rawURL := strings.TrimSpace(r.FormValue("video_url"))
	catID, err := strconv.Atoi(r.FormValue("category_id"))
	if err != nil || catID < 1 || len([]rune(title)) < 3 || len([]rune(title)) > 200 || len(body) > 8000 {
		http.Error(w, "Sarlavha, fan yoki tavsif noto'g'ri", 400)
		return
	}
	id, ok := itubeYouTubeID(rawURL)
	if !ok {
		http.Error(w, "Hozircha YouTube video havolasini kiriting", 400)
		return
	}
	normalizedURL := "https://www.youtube.com/watch?v=" + id
	status := "DR" // An authorized video submitter may choose to publish.
	if r.FormValue("status") == "PB" {
		status = "PB"
	}
	var exists bool
	if err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM news_app_new_video WHERE video_url=?)`, normalizedURL).Scan(&exists); err != nil {
		http.Error(w, "Takrorni tekshirib bo'lmadi", 500)
		return
	}
	if exists {
		http.Error(w, "Bu YouTube video allaqachon qo'shilgan", 409)
		return
	}
	tx, err := a.db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Baza xatosi", 500)
		return
	}
	defer tx.Rollback()
	var videoID int64
	err = tx.QueryRowContext(r.Context(), `INSERT INTO news_app_new_video(title,slug,body,video_url,image,category_id,status,created_time)
        VALUES ($1,'',$2,$3,$4,$5,$6,NOW()) RETURNING id`, title, body, normalizedURL,
		"https://img.youtube.com/vi/"+id+"/hqdefault.jpg", catID, status).Scan(&videoID)
	if err != nil {
		log.Printf("insert video: %v", err)
		http.Error(w, "Video qo'shilmadi; fan tanlanganini tekshiring", 500)
		return
	}
	slug := itubeVideoSlug(title, videoID)
	if _, err = tx.ExecContext(r.Context(), `UPDATE news_app_new_video SET slug=$1 WHERE id=$2`, slug, videoID); err != nil {
		http.Error(w, "Video manzili saqlanmadi", 500)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Saqlash xatosi", 500)
		return
	}
	http.Redirect(w, r, "/admin/videos?added=1", http.StatusSeeOther)
}
func (a *App) adminEditVideoHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	video, _ := a.videoByID(id)
	cats, _ := a.allCategories()
	a.render(w, r, "admin_edit_video.html", map[string]any{"Video": video, "Categories": cats})
}
func (a *App) adminUpdateVideoHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	videoURL := strings.TrimSpace(r.FormValue("video_url"))
	categoryID := r.FormValue("category_id")
	status := r.FormValue("status")
	slug := itubeVideoSlug(title, int64(id))
	imageURL := ""
	if isYouTube(videoURL) {
		imageURL = youtubeThumb(videoURL)
	}
	_, err := a.db.Exec(`
		UPDATE news_app_new_video SET title=?, slug=?, body=?, video_url=?, image=?, category_id=?, status=? WHERE id=?
	`, title, slug, body, videoURL, imageURL, categoryID, status, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/videos", http.StatusSeeOther)
}
func (a *App) adminDeleteVideoHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	a.db.Exec(`DELETE FROM news_app_new_video WHERE id=?`, id)
	http.Redirect(w, r, "/admin/videos", http.StatusSeeOther)
}

// ===== ADMIN MANAGE =====
func (a *App) adminManageHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	type ClassItem struct {
		ID           int
		ClassName    string
		ClassLeader  string
		ClassTeacher string
		StudentCount int
	}
	type StudentItem struct {
		ID        int
		FullName  string
		BirthYear int
		Gender    string
		Address   string
		Origin    string
	}
	type SchoolItem struct {
		ID         int
		SchoolName string
		Region     string
		ClassCount int
		Classes    []ClassItem
	}

	rows, err := a.db.Query(`
		SELECT sp.id, sp.school_name, sp.region
		FROM school_profile sp
		JOIN auth_user u ON u.id = sp.user_id
		ORDER BY sp.region, sp.school_name
	`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	var schools []SchoolItem
	for rows.Next() {
		var s SchoolItem
		rows.Scan(&s.ID, &s.SchoolName, &s.Region)
		a.db.QueryRow(`SELECT COUNT(*) FROM class_profile WHERE school_id=?`, s.ID).Scan(&s.ClassCount)
		schools = append(schools, s)
	}

	var selectedSchool *SchoolItem
	var selectedClass *ClassItem
	var classStudents []StudentItem

	sidStr := r.URL.Query().Get("school_id")
	cidStr := r.URL.Query().Get("class_id")

	if sidStr != "" {
		sid, _ := strconv.Atoi(sidStr)
		for i := range schools {
			if schools[i].ID == sid {
				classRows, _ := a.db.Query(`
					SELECT id, class_name, class_leader, class_teacher
					FROM class_profile WHERE school_id=?
					ORDER BY class_number, class_letter
				`, sid)
				if classRows != nil {
					for classRows.Next() {
						var c ClassItem
						classRows.Scan(&c.ID, &c.ClassName, &c.ClassLeader, &c.ClassTeacher)
						a.db.QueryRow(`SELECT COUNT(*) FROM students WHERE class_id=?`, c.ID).Scan(&c.StudentCount)
						schools[i].Classes = append(schools[i].Classes, c)
					}
					classRows.Close()
				}
				selectedSchool = &schools[i]
				break
			}
		}
	}

	if cidStr != "" && selectedSchool != nil {
		cid, _ := strconv.Atoi(cidStr)
		for i := range selectedSchool.Classes {
			if selectedSchool.Classes[i].ID == cid {
				selectedClass = &selectedSchool.Classes[i]
				rows, _ := a.db.Query(`
					SELECT id, full_name, COALESCE(birth_year,0), COALESCE(gender,''), COALESCE(address,''), COALESCE(origin,'')
					FROM students WHERE class_id=? ORDER BY full_name
				`, cid)
				if rows != nil {
					for rows.Next() {
						var s StudentItem
						rows.Scan(&s.ID, &s.FullName, &s.BirthYear, &s.Gender, &s.Address, &s.Origin)
						classStudents = append(classStudents, s)
					}
					rows.Close()
				}
				break
			}
		}
	}

	a.render(w, r, "admin_manage.html", map[string]any{
		"Schools":        schools,
		"SelectedSchool": selectedSchool,
		"SelectedClass":  selectedClass,
		"ClassStudents":  classStudents,
	})
}

func (a *App) adminAddSchoolHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	schoolName := strings.TrimSpace(r.FormValue("school_name"))
	region := strings.TrimSpace(r.FormValue("region"))
	if schoolName == "" || region == "" {
		http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
		return
	}
	plainPassword := r.FormValue("school_password")
	if len([]rune(plainPassword)) < 12 {
		http.Error(w, "Parol kamida 12 belgidan iborat bo'lishi kerak", 400)
		return
	}

	hashed, err := djangoHashPassword(plainPassword, 720000)
	if err != nil {
		http.Error(w, "Parol xatosi", 500)
		return
	}

	tx, err := a.db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Database xatosi", 500)
		return
	}
	defer tx.Rollback()

	var userID int
	err = tx.QueryRow(`
        INSERT INTO auth_user
        (username, password, email, region, role, is_staff, is_active)
        VALUES ($1, $2, '', $3, 'school', 0, 1)
        RETURNING id
    `, schoolName, hashed, region).Scan(&userID)

	if err != nil {
		http.Error(w, "Maktab logini mavjud yoki saqlashda xatolik", 409)
		return
	}

	_, err = tx.Exec(`
        INSERT INTO school_profile (user_id, school_name, region)
        VALUES ($1, $2, $3)
    `, userID, schoolName, region)

	if err != nil {
		http.Error(w, "Maktab profilini yaratib bo'lmadi", 500)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Saqlashda xatolik", 500)
		return
	}

	http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
}

func (a *App) adminAddClassHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Forma xatosi", 400)
		return
	}
	if !a.verifyCSRFToken(r) {
		http.Error(w, "CSRF xatosi", 400)
		return
	}
	schoolIDStr := r.FormValue("school_id")
	schoolID, err := strconv.Atoi(schoolIDStr)
	classNumber := strings.TrimSpace(r.FormValue("class_number"))
	classLetter := strings.ToUpper(strings.TrimSpace(r.FormValue("class_letter")))
	classLeader := strings.TrimSpace(r.FormValue("class_leader"))
	classTeacher := strings.TrimSpace(r.FormValue("class_teacher"))
	plainPassword := r.FormValue("class_password")
	if err != nil || schoolID < 1 || classNumber == "" || classLetter == "" || classLeader == "" || classTeacher == "" {
		http.Error(w, "Sinf ma'lumotlari to'liq emas", 400)
		return
	}
	if len([]rune(plainPassword)) < 12 {
		http.Error(w, "Parol kamida 12 belgili bo'lsin", 400)
		return
	}
	hash, err := djangoHashPassword(plainPassword, 720000)
	if err != nil {
		http.Error(w, "Parolni xeshlashda xato", 500)
		return
	}
	tx, err := a.db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Database xatosi", 500)
		return
	}
	defer tx.Rollback()
	var classID int
	err = tx.QueryRow(`
        INSERT INTO class_profile (school_id,class_number,class_letter,class_leader,class_teacher)
        VALUES ($1,$2,$3,$4,$5) RETURNING id
    `, schoolID, classNumber, classLetter, classLeader, classTeacher).Scan(&classID)
	if err != nil {
		http.Error(w, "Sinf mavjud yoki maktab noto'g'ri", 409)
		return
	}
	// ITUBE_CLASS_ACCOUNTS_V2: O'qituvchi ismi login sifatida ishlatilmaydi.
	classUsername := fmt.Sprintf("class_%d", classID)
	_, err = tx.Exec(`
        INSERT INTO auth_user (username,password,email,region,role,is_staff,is_active)
        VALUES ($1,$2,'','','class',0,1)
    `, classUsername, hash)
	if err != nil {
		http.Error(w, "Sinf hisobini saqlashda xato", 409)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "Saqlashda xato", 500)
		return
	}
	http.Redirect(w, r, "/admin/manage?school_id="+schoolIDStr, http.StatusSeeOther)
}

func (a *App) adminAddStudentHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	classIDStr := r.FormValue("class_id")
	classID, _ := strconv.Atoi(classIDStr)
	schoolIDStr := r.FormValue("school_id")
	fullName := strings.TrimSpace(r.FormValue("full_name"))
	birthYearStr := strings.TrimSpace(r.FormValue("birth_year"))
	gender := strings.TrimSpace(r.FormValue("gender"))
	address := strings.TrimSpace(r.FormValue("address"))
	origin := strings.TrimSpace(r.FormValue("origin"))

	if classID == 0 || fullName == "" {
		http.Redirect(w, r, "/admin/manage?school_id="+schoolIDStr, http.StatusSeeOther)
		return
	}
	birthYear, _ := strconv.Atoi(birthYearStr)

	_, err := a.db.Exec(`
		INSERT INTO students (class_id, full_name, birth_year, gender, address, origin)
		VALUES (?, ?, ?, ?, ?, ?)
	`, classID, fullName, birthYear, gender, address, origin)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/manage?school_id="+schoolIDStr+"&class_id="+classIDStr, http.StatusSeeOther)
}

func (a *App) adminDeleteSchoolHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	var userID int
	a.db.QueryRow(`SELECT user_id FROM school_profile WHERE id=?`, id).Scan(&userID)
	a.db.Exec(`DELETE FROM school_profile WHERE id=?`, id)
	if userID > 0 {
		a.db.Exec(`DELETE FROM auth_user WHERE id=?`, userID)
	}
	http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
}

func (a *App) adminDeleteClassHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	var schoolID int
	a.db.QueryRow(`SELECT school_id FROM class_profile WHERE id=?`, id).Scan(&schoolID)
	a.db.Exec(`DELETE FROM class_profile WHERE id=?`, id)
	http.Redirect(w, r, "/admin/manage?school_id="+strconv.Itoa(schoolID), http.StatusSeeOther)
}

func (a *App) adminDeleteStudentHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	r.ParseForm()
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	var classID, schoolID int
	a.db.QueryRow(`SELECT cp.class_id, c.school_id FROM students cp JOIN class_profile c ON c.id=cp.class_id WHERE cp.id=?`, id).Scan(&classID, &schoolID)
	a.db.Exec(`DELETE FROM students WHERE id=?`, id)
	http.Redirect(w, r, "/admin/manage?school_id="+strconv.Itoa(schoolID)+"&class_id="+strconv.Itoa(classID), http.StatusSeeOther)
}

// ===== ADMIN SCHOOLS =====
func (a *App) adminSchoolsHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	rows, err := a.db.Query(`
		SELECT u.id, u.username, u.email, u.region, u.role, u.is_staff,
		       (SELECT COUNT(*) FROM class_profile WHERE school_id = (SELECT id FROM school_profile WHERE user_id=u.id)) as class_count
		FROM auth_user u WHERE u.role='school' ORDER BY u.id DESC
	`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type UserWithSchool struct {
		User
		ClassCount int
	}
	var users []UserWithSchool
	for rows.Next() {
		var u User
		var classCnt int
		rows.Scan(&u.ID, &u.Username, &u.Email, &u.Region, &u.Role, &u.IsStaff, &classCnt)
		users = append(users, UserWithSchool{User: u, ClassCount: classCnt})
	}
	a.render(w, r, "admin_school.html", map[string]any{"Users": users})
}

func (a *App) adminSchoolDetailHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	userID, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var school SchoolProfile
	err := a.db.QueryRow(`SELECT id, user_id, school_name, region FROM school_profile WHERE user_id=?`, userID).
		Scan(&school.ID, &school.UserID, &school.SchoolName, &school.Region)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	type LessonVideoStat struct {
		ID            int
		VideoTitle    string
		Subject       string
		Hour          int
		StudentsTotal int
		ActiveCount   int
		Percent       int
		CreatedAt     string
	}
	type ClassStats struct {
		ClassProfile
		LessonVideos   []LessonVideoStat
		TotalStudents  int
		DailyPercent   int
		MonthlyPercent int
		YearlyPercent  int
		VideosToday    int
		VideosMonth    int
		VideosYear     int
		TodayLessons   int
		MonthlyMax     int
		YearlyMax      int
	}

	rows, _ := a.db.Query(`
		SELECT id, class_number, class_letter, class_leader, class_teacher, class_name
		FROM class_profile WHERE school_id=?
		ORDER BY class_number, class_letter
	`, school.ID)
	defer rows.Close()

	var classes []ClassStats
	for rows.Next() {
		var c ClassProfile
		rows.Scan(&c.ID, &c.ClassNumber, &c.ClassLetter, &c.ClassLeader, &c.ClassTeacher, &c.ClassName)

		var totalStudents int
		a.db.QueryRow(`SELECT COUNT(*) FROM students WHERE class_id=?`, c.ID).Scan(&totalStudents)

		vidRows, _ := a.db.Query(`
			SELECT ls.id, COALESCE(v.title, 'Noma''lum video'), ls.subject, ls.hour,
			       ls.students_total, ls.active_count, CAST(ls.created_at AS TEXT)
			FROM lesson_stats ls
			LEFT JOIN news_app_new_video v ON v.id = ls.video_id
			WHERE ls.class_id=?
			ORDER BY ls.created_at DESC
		`, c.ID)
		var lessonVideos []LessonVideoStat
		if vidRows != nil {
			for vidRows.Next() {
				var lv LessonVideoStat
				vidRows.Scan(&lv.ID, &lv.VideoTitle, &lv.Subject, &lv.Hour,
					&lv.StudentsTotal, &lv.ActiveCount, &lv.CreatedAt)
				if lv.StudentsTotal > 0 {
					lv.Percent = int(float64(lv.ActiveCount) / float64(lv.StudentsTotal) * 100)
				}
				lessonVideos = append(lessonVideos, lv)
			}
			vidRows.Close()
		}

		// Bugungi kalendar kunidagi ko'rilgan videolar
		var videosToday, videosMonth, videosYear int
		a.db.QueryRow(`
			SELECT COUNT(*) FROM lesson_stats 
			WHERE class_id=? AND created_at::date = CURRENT_DATE
		`, c.ID).Scan(&videosToday)
		a.db.QueryRow(`
			SELECT COUNT(*) FROM lesson_stats 
			WHERE class_id=? AND created_at >= CURRENT_DATE - INTERVAL '30 days'
		`, c.ID).Scan(&videosMonth)
		a.db.QueryRow(`
			SELECT COUNT(*) FROM lesson_stats 
			WHERE class_id=? AND created_at >= CURRENT_DATE - INTERVAL '270 days'
		`, c.ID).Scan(&videosYear)

		// Dars jadvalini mapga olish
		// Kalitlar: 0=Yakshanba, 1=Dushanba, ..., 6=Shanba
		// Yakshanba jadvalga yozilmaydi, shuning uchun map'da yo'q Р Р†РІР‚В РІР‚в„ў 0 qaytaradi
		scheduleMap := map[int]int{}
		sRows, _ := a.db.Query(`SELECT day_of_week, lesson_count FROM class_schedule WHERE class_id=?`, c.ID)
		if sRows != nil {
			for sRows.Next() {
				var d, cnt int
				sRows.Scan(&d, &cnt)
				scheduleMap[d] = cnt
			}
			sRows.Close()
		}

		// Bugungi hafta kuni uchun darslar soni (Yakshanba = 0)
		now := time.Now()
		goWeekday := int(now.Weekday()) // 0=Yakshanba, 1=Dushanba, ..., 6=Shanba
		todayLessons := scheduleMap[goWeekday]

		// Oylik va yillik maksimal: oxirgi 30 va 270 kalendar kun ichida
		// jadval bo'yicha jami darslar soni. Yakshanba kunlari avtomatik 0.
		monthlyMax := 0
		for i := 0; i < 30; i++ {
			d := now.AddDate(0, 0, -i)
			monthlyMax += scheduleMap[int(d.Weekday())]
		}
		yearlyMax := 0
		for i := 0; i < 270; i++ {
			d := now.AddDate(0, 0, -i)
			yearlyMax += scheduleMap[int(d.Weekday())]
		}

		dailyPct := 0
		if todayLessons > 0 {
			dailyPct = videosToday * 100 / todayLessons
			if dailyPct > 100 {
				dailyPct = 100
			}
		}

		monthlyPct := 0
		if monthlyMax > 0 {
			monthlyPct = videosMonth * 100 / monthlyMax
			if monthlyPct > 100 {
				monthlyPct = 100
			}
		}

		yearlyPct := 0
		if yearlyMax > 0 {
			yearlyPct = videosYear * 100 / yearlyMax
			if yearlyPct > 100 {
				yearlyPct = 100
			}
		}

		classes = append(classes, ClassStats{
			ClassProfile:   c,
			LessonVideos:   lessonVideos,
			TotalStudents:  totalStudents,
			DailyPercent:   dailyPct,
			MonthlyPercent: monthlyPct,
			YearlyPercent:  yearlyPct,
			VideosToday:    videosToday,
			VideosMonth:    videosMonth,
			VideosYear:     videosYear,
			TodayLessons:   todayLessons,
			MonthlyMax:     monthlyMax,
			YearlyMax:      yearlyMax,
		})
	}

	a.render(w, r, "admin_school_detail.html", map[string]any{
		"School":  school,
		"Classes": classes,
	})
}

// ===== MY CLASSES =====
func (a *App) myClassesHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.currentUserID(r); !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	role := a.currentUserRole(r)
	if role != "school" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	userID, _ := a.currentUserID(r)
	var school SchoolProfile
	err := a.db.QueryRow(`SELECT id, user_id, school_name, region FROM school_profile WHERE user_id=?`, userID).Scan(&school.ID, &school.UserID, &school.SchoolName, &school.Region)
	if err != nil {
		http.Error(w, "Maktab profili topilmadi", 404)
		return
	}
	rows, _ := a.db.Query(`SELECT id, class_number, class_letter, class_leader, class_teacher, class_name FROM class_profile WHERE school_id=?`, school.ID)
	defer rows.Close()
	type SubjectStat struct {
		Subject       string
		Hour          int
		StudentsTotal int
		ActiveCount   int
		AvgPercent    int
	}
	type ClassWithSubjects struct {
		ClassProfile
		Subjects map[string][]SubjectStat
	}
	var classes []ClassWithSubjects
	for rows.Next() {
		var c ClassProfile
		rows.Scan(&c.ID, &c.ClassNumber, &c.ClassLetter, &c.ClassLeader, &c.ClassTeacher, &c.ClassName)
		rowsStats, _ := a.db.Query(`
			SELECT subject, hour, students_total, active_count,
				ROUND(CAST(active_count AS FLOAT) / NULLIF(students_total,0) * 100) as avg_percent
			FROM lesson_stats WHERE class_id=? ORDER BY subject, hour
		`, c.ID)
		subjectsMap := make(map[string][]SubjectStat)
		if rowsStats != nil {
			for rowsStats.Next() {
				var subj string
				var hour, st, act, avg int
				rowsStats.Scan(&subj, &hour, &st, &act, &avg)
				subjectsMap[subj] = append(subjectsMap[subj], SubjectStat{Subject: subj, Hour: hour, StudentsTotal: st, ActiveCount: act, AvgPercent: avg})
			}
			rowsStats.Close()
		}
		classes = append(classes, ClassWithSubjects{ClassProfile: c, Subjects: subjectsMap})
	}
	a.render(w, r, "admin_schools.html", map[string]any{
		"SchoolName": school.SchoolName,
		"Region":     school.Region,
		"Classes":    classes,
	})
}

// ===== ADMIN USERS =====
func (a *App) adminUsersHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	rows, err := a.db.Query(`SELECT u.id,u.username,u.email,u.email_verified,u.region,u.role,
      (SELECT COUNT(*) FROM user_progress WHERE user_id=u.id) AS videos_watched,
      (SELECT AVG(watched_percent) FROM user_progress WHERE user_id=u.id) AS avg_percent
      FROM auth_user u WHERE u.role='user' AND u.is_staff=0 ORDER BY u.id DESC`)
	if err != nil {
		log.Printf("admin users query: %v", err)
		http.Error(w, "Foydalanuvchilarni yuklab bo'lmadi", 500)
		return
	}
	defer rows.Close()
	type StudentInfo struct {
		User
		VideosWatched int
		AvgPercent    int
	}
	users := make([]StudentInfo, 0)
	for rows.Next() {
		var u StudentInfo
		var avg sql.NullFloat64
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.EmailVerified, &u.Region, &u.Role, &u.VideosWatched, &avg); err != nil {
			http.Error(w, "Foydalanuvchilarni o'qishda xato", 500)
			return
		}
		if avg.Valid {
			u.AvgPercent = int(avg.Float64)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "Foydalanuvchilarni o'qishda xato", 500)
		return
	}
	a.render(w, r, "admin_users.html", map[string]any{"Students": users})
}

// ===== HELPERS =====
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
func djangoHashPassword(raw string, iter int) (string, error) {
	salt, _ := randomSalt(16)
	hash := pbkdf2.Key([]byte(raw), []byte(salt), iter, 32, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iter, salt, base64.StdEncoding.EncodeToString(hash)), nil
}
func djangoCheckPassword(encoded, raw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return false
	}
	iter, _ := strconv.Atoi(parts[1])
	salt := parts[2]
	expected := parts[3]
	hash := pbkdf2.Key([]byte(raw), []byte(salt), iter, 32, sha256.New)
	return expected == base64.StdEncoding.EncodeToString(hash)
}
func randomSalt(n int) (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}
func slugify(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = strings.ReplaceAll(title, " ", "-")
	replacer := strings.NewReplacer("'", "", "\"", "", "?", "", "!", "", ".", "", ",", "", "&", "and")
	title = replacer.Replace(title)
	return title
}
