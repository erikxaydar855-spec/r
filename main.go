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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"
	"golang.org/x/crypto/pbkdf2"
	_ "modernc.org/sqlite"
)

const (
	MIN_VIDEOS_PER_DAY   = 3
	MIN_VIDEOS_PER_MONTH = 60
	MIN_VIDEOS_PER_YEAR  = 540
)

type App struct {
	db        *sql.DB
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
func isYouTube(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

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
	if strings.Contains(rawURL, "youtu.be") {
		parts := strings.Split(rawURL, "/")
		if len(parts) > 0 {
			id := parts[len(parts)-1]
			if idx := strings.IndexAny(id, "?&"); idx != -1 {
				id = id[:idx]
			}
			return id
		}
	}
	if strings.Contains(rawURL, "watch?v=") {
		parts := strings.Split(rawURL, "v=")
		if len(parts) > 1 {
			id := parts[1]
			if idx := strings.IndexAny(id, "?&"); idx != -1 {
				id = id[:idx]
			}
			return id
		}
	}
	if strings.Contains(rawURL, "/embed/") {
		parts := strings.Split(rawURL, "/embed/")
		if len(parts) > 1 {
			id := parts[1]
			if idx := strings.IndexAny(id, "?&"); idx != -1 {
				id = id[:idx]
			}
			return id
		}
	}
	return ""
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
	return string(runes[:n]) + "…"
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
	db, err := sql.Open("sqlite", "db.sqlite3?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}
	if err := ensureSchema(db); err != nil {
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

func ensureSchema(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS auth_user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE,
			password TEXT,
			email TEXT,
			region TEXT,
			role TEXT DEFAULT 'user',
			is_staff INTEGER DEFAULT 0,
			is_active INTEGER DEFAULT 1
		);`,
		`CREATE TABLE IF NOT EXISTS news_app_category (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE
		);`,
		`CREATE TABLE IF NOT EXISTS news_app_new_video (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT,
			slug TEXT,
			body TEXT,
			video_url TEXT,
			image TEXT,
			status TEXT,
			category_id INTEGER,
			created_time DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (category_id) REFERENCES news_app_category(id)
		);`,
		`CREATE TABLE IF NOT EXISTS school_profile (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER UNIQUE,
			school_name TEXT,
			region TEXT,
			FOREIGN KEY (user_id) REFERENCES auth_user(id)
		);`,
		`CREATE TABLE IF NOT EXISTS class_profile (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			school_id INTEGER NOT NULL,
			class_number INTEGER NOT NULL,
			class_letter TEXT NOT NULL,
			class_leader TEXT NOT NULL,
			class_teacher TEXT NOT NULL,
			class_name TEXT GENERATED ALWAYS AS (class_number || class_letter) VIRTUAL,
			UNIQUE(school_id, class_number, class_letter),
			FOREIGN KEY (school_id) REFERENCES school_profile(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS students (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			class_id INTEGER NOT NULL,
			full_name TEXT NOT NULL,
			birth_year INTEGER,
			gender TEXT,
			address TEXT,
			origin TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (class_id) REFERENCES class_profile(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS lesson_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			class_id INTEGER NOT NULL,
			hour INTEGER NOT NULL,
			subject TEXT NOT NULL,
			students_total INTEGER NOT NULL,
			active_count INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(class_id) REFERENCES class_profile(id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_lesson_unique ON lesson_stats(class_id, hour, subject);`,
		`CREATE TABLE IF NOT EXISTS lesson_active (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
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
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, video_id),
			FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE,
			FOREIGN KEY(video_id) REFERENCES news_app_new_video(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS ai_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER,
			query TEXT,
			response TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id INTEGER PRIMARY KEY,
			avatar TEXT,
			FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS class_schedule (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
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
	db.Exec(`ALTER TABLE lesson_stats ADD COLUMN video_id INTEGER DEFAULT 0`)

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
		db.Exec(`INSERT OR IGNORE INTO news_app_category (name) VALUES (?)`, cat)
	}

	hashed, _ := djangoHashPassword("admin123", 720000)
	_, err := db.Exec(`
		INSERT INTO auth_user (username, password, email, region, role, is_staff, is_active) 
		VALUES (?, ?, 'admin@itube.uz', 'Toshkent', 'school', 1, 1)
		ON CONFLICT(username) DO UPDATE SET password=excluded.password, is_staff=1
	`, "admin", hashed)
	if err != nil {
		return err
	}

	var adminID int
	db.QueryRow(`SELECT id FROM auth_user WHERE username='admin'`).Scan(&adminID)
	if adminID > 0 {
		db.Exec(`INSERT OR IGNORE INTO school_profile (user_id, school_name, region) VALUES (?, 'Admin maktabi', 'Toshkent')`, adminID)
	}

	return nil
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
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
	r.Get("/api/class-students", a.apiClassStudentsHandler)

	r.Get("/profile", a.profileHandler)
	r.Post("/profile/avatar", a.uploadAvatarHandler)
	r.Post("/profile/password", a.updatePasswordHandler)
	r.Post("/profile/class", a.updateClassHandler)

	r.Get("/admin/videos", a.adminVideosHandler)
	r.Get("/admin/videos/", a.adminVideosHandler)
	r.Post("/admin/videos/add", a.adminAddVideoHandler)
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

	if username == "" || len(password) < 6 {
		a.render(w, r, "register.html", map[string]any{"Error": "Username kiriting va parol kamida 6 belgi bo‘lishi kerak."})
		return
	}
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM auth_user WHERE username=?`, username).Scan(&count)
	if count > 0 {
		a.render(w, r, "register.html", map[string]any{"Error": "Bu username allaqachon band."})
		return
	}
	hashed, _ := djangoHashPassword(password, 720000)
	res, err := a.db.Exec(`
		INSERT INTO auth_user (username, password, email, region, role, is_staff, is_active)
		VALUES (?, ?, '', '', 'user', 0, 1)
	`, username, hashed)
	if err != nil {
		a.render(w, r, "register.html", map[string]any{"Error": "Xatolik yuz berdi."})
		return
	}
	userID, _ := res.LastInsertId()
	session, _ := a.store.Get(r, "itube_session")
	session.Values["user_id"] = int(userID)
	session.Values["username"] = username
	session.Values["is_staff"] = false
	session.Values["role"] = "user"
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	if err != nil || !djangoCheckPassword(user.Password, password) {
		a.render(w, r, "auth.html", map[string]any{"Error": "Noto‘g‘ri username yoki parol."})
		return
	}
	session, _ := a.store.Get(r, "itube_session")
	session.Values["user_id"] = user.ID
	session.Values["username"] = user.Username
	session.Values["is_staff"] = isStaff == 1
	session.Values["role"] = role
	session.Save(r, w)
	if isStaff == 1 {
		http.Redirect(w, r, "/admin/videos", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (a *App) schoolLoginHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if !a.verifyCSRFToken(r) {
		http.Error(w, "csrf", 400)
		return
	}
	schoolIDStr := strings.TrimSpace(r.FormValue("school_id"))
	password := strings.TrimSpace(r.FormValue("password"))
	if schoolIDStr == "" || password == "" {
		a.render(w, r, "auth.html", map[string]any{"Error": "Maktabni tanlang va parol kiriting."})
		return
	}
	schoolID, err := strconv.Atoi(schoolIDStr)
	if err != nil || schoolID == 0 {
		a.render(w, r, "auth.html", map[string]any{"Error": "Maktabni to‘g‘ri tanlang."})
		return
	}
	var schoolUserID int
	var schoolName string
	err = a.db.QueryRow(`SELECT user_id, school_name FROM school_profile WHERE id=?`, schoolID).Scan(&schoolUserID, &schoolName)
	if err != nil {
		a.render(w, r, "auth.html", map[string]any{"Error": "Maktab topilmadi."})
		return
	}
	num := extractNumber(schoolName)
	schoolPassword := num + "maktab"

	if password == schoolPassword {
		var userID int
		var username string
		var isStaff int
		err = a.db.QueryRow(`SELECT id, username, is_staff FROM auth_user WHERE id=?`, schoolUserID).Scan(&userID, &username, &isStaff)
		if err != nil {
			a.render(w, r, "auth.html", map[string]any{"Error": "Maktab akkaunti topilmadi."})
			return
		}
		session, _ := a.store.Get(r, "itube_session")
		session.Values["user_id"] = userID
		session.Values["username"] = username
		session.Values["is_staff"] = isStaff == 1
		session.Values["role"] = "school"
		session.Save(r, w)
		http.Redirect(w, r, "/myclasses", http.StatusSeeOther)
		return
	}

	rows, err := a.db.Query(`SELECT id, class_name, class_teacher FROM class_profile WHERE school_id=?`, schoolID)
	if err != nil {
		a.render(w, r, "auth.html", map[string]any{"Error": "Sinflar topilmadi."})
		return
	}
	defer rows.Close()
	var matchedTeacher string
	var matchedClassID int
	for rows.Next() {
		var cid int
		var cname, cteacher string
		if err := rows.Scan(&cid, &cname, &cteacher); err != nil {
			continue
		}
		if password == cname+"sinf" {
			matchedTeacher = cteacher
			matchedClassID = cid
			break
		}
	}
	rows.Close()

	if matchedTeacher == "" {
		a.render(w, r, "auth.html", map[string]any{"Error": "Parol noto‘g‘ri."})
		return
	}
	var userID int
	var username string
	var isStaff int
	err = a.db.QueryRow(`SELECT id, username, is_staff FROM auth_user WHERE username=?`, matchedTeacher).Scan(&userID, &username, &isStaff)
	if err != nil {
		a.render(w, r, "auth.html", map[string]any{"Error": "Sinf akkaunti topilmadi."})
		return
	}
	session, _ := a.store.Get(r, "itube_session")
	session.Values["user_id"] = userID
	session.Values["username"] = username
	session.Values["is_staff"] = isStaff == 1
	session.Values["role"] = "class"
	session.Values["class_id"] = matchedClassID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
		WHERE v.status='PB' AND (v.title LIKE ? OR v.body LIKE ? OR c.name LIKE ?)
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
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(user_id, video_id) DO UPDATE SET
			watched_percent = excluded.watched_percent,
			updated_at = datetime('now')
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
			UPDATE lesson_stats SET students_total=?, active_count=?, video_id=?, created_at=datetime('now')
			WHERE id=?
		`, totalStudents, activeCount, payload.VideoID, lessonID)
		a.db.Exec(`DELETE FROM lesson_active WHERE lesson_id=?`, lessonID)
	} else {
		res, err := a.db.Exec(`
			INSERT INTO lesson_stats (class_id, hour, subject, video_id, students_total, active_count, created_at)
			VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		`, classID, payload.Hour, payload.Subject, payload.VideoID, totalStudents, activeCount)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		lid, _ := res.LastInsertId()
		lessonID = int(lid)
	}

	for _, sid := range payload.ActiveIDs {
		a.db.Exec(`INSERT OR IGNORE INTO lesson_active (lesson_id, student_id) VALUES (?, ?)`, lessonID, sid)
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
	a.db.Exec(`INSERT INTO ai_logs (user_id, query, response, created_at) VALUES (?, ?, ?, datetime('now'))`, userID, finalQuery, resp)
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
			WHERE cp.class_teacher = (SELECT username FROM auth_user WHERE id=?)
		`, userID).Scan(&className, &classNum, &classLetter, &classLeader)
		schoolOrClass = className
	}
	a.render(w, r, "profile.html", map[string]any{
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
	a.db.Exec(`INSERT OR REPLACE INTO user_profiles (user_id, avatar) VALUES (?, ?)`, userID, avatarPath)
	writeJSON(w, map[string]any{"status": "ok", "avatar": avatarPath})
}

func (a *App) updatePasswordHandler(w http.ResponseWriter, r *http.Request) {
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
		Current string `json:"current"`
		New     string `json:"new"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	var hashed string
	a.db.QueryRow(`SELECT password FROM auth_user WHERE id=?`, userID).Scan(&hashed)
	if !djangoCheckPassword(hashed, payload.Current) {
		writeJSON(w, map[string]any{"status": "error", "error": "Joriy parol noto'g'ri"})
		return
	}
	if len(payload.New) < 6 {
		writeJSON(w, map[string]any{"status": "error", "error": "Yangi parol 6+ belgi"})
		return
	}
	newHashed, _ := djangoHashPassword(payload.New, 720000)
	a.db.Exec(`UPDATE auth_user SET password=? WHERE id=?`, newHashed, userID)
	writeJSON(w, map[string]any{"status": "ok"})
}

func (a *App) updateClassHandler(w http.ResponseWriter, r *http.Request) {
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
		ClassNumber int    `json:"class_number"`
		ClassLetter string `json:"class_letter"`
		ClassLeader string `json:"class_leader"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	_, err := a.db.Exec(`
		UPDATE class_profile
		SET class_number=?, class_letter=?, class_leader=?
		WHERE class_teacher = (SELECT username FROM auth_user WHERE id=?)
	`, payload.ClassNumber, payload.ClassLetter, payload.ClassLeader, userID)
	if err != nil {
		writeJSON(w, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

// ===== ADMIN VIDEOS =====
func (a *App) adminVideosHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	videos, _ := a.allVideos()
	cats, _ := a.allCategories()
	total := len(videos)
	pub, draft := 0, 0
	for _, v := range videos {
		if v.Status == "PB" {
			pub++
		} else {
			draft++
		}
	}
	a.render(w, r, "admin_videos.html", map[string]any{
		"Videos": videos, "Categories": cats,
		"TotalVideos": total, "PublishedCount": pub, "DraftCount": draft,
	})
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
		rows.Scan(&v.ID, &v.Title, &v.Slug, &v.Body, &url, &img, &v.Status, &v.CreatedTime, &v.CategoryID, &v.CategoryName)
		v.VideoURL = url.String
		v.Image = img.String
		videos = append(videos, v)
	}
	return videos, nil
}
func (a *App) adminAddVideoHandler(w http.ResponseWriter, r *http.Request) {
	if !a.currentUserIsStaff(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
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
	if status == "" {
		status = "DR"
	}
	slug := slugify(title)
	imageURL := ""
	if isYouTube(videoURL) {
		imageURL = youtubeThumb(videoURL)
	}
	_, err := a.db.Exec(`
		INSERT INTO news_app_new_video (title, slug, body, video_url, image, category_id, status, created_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
	`, title, slug, body, videoURL, imageURL, categoryID, status)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/videos", http.StatusSeeOther)
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
	slug := slugify(title)
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
		ID            int
		ClassName     string
		ClassLeader   string
		ClassTeacher  string
		ClassPassword string
		StudentCount  int
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
		ID             int
		SchoolName     string
		Region         string
		SchoolPassword string
		ClassCount     int
		Classes        []ClassItem
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
		s.SchoolPassword = extractNumber(s.SchoolName) + "maktab"
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
						c.ClassPassword = c.ClassName + "sinf"
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
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM auth_user WHERE username=?`, schoolName).Scan(&count)
	if count > 0 {
		http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
		return
	}
	plainPassword := extractNumber(schoolName) + "maktab"
	hashed, _ := djangoHashPassword(plainPassword, 720000)
	res, err := a.db.Exec(`
		INSERT INTO auth_user (username, password, email, region, role, is_staff, is_active)
		VALUES (?, ?, '', ?, 'school', 0, 1)
	`, schoolName, hashed, region)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	userID, _ := res.LastInsertId()
	a.db.Exec(`INSERT INTO school_profile (user_id, school_name, region) VALUES (?, ?, ?)`, userID, schoolName, region)
	http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
}

func (a *App) adminAddClassHandler(w http.ResponseWriter, r *http.Request) {
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
	schoolID, _ := strconv.Atoi(schoolIDStr)
	classNumber := strings.TrimSpace(r.FormValue("class_number"))
	classLetter := strings.ToUpper(strings.TrimSpace(r.FormValue("class_letter")))
	classLeader := strings.TrimSpace(r.FormValue("class_leader"))
	classTeacher := strings.TrimSpace(r.FormValue("class_teacher"))
	if schoolID == 0 || classNumber == "" || classLetter == "" || classLeader == "" || classTeacher == "" {
		http.Redirect(w, r, "/admin/manage", http.StatusSeeOther)
		return
	}
	var userExists int
	a.db.QueryRow(`SELECT COUNT(*) FROM auth_user WHERE username=?`, classTeacher).Scan(&userExists)
	if userExists == 0 {
		plainPassword := classNumber + classLetter + "sinf"
		hashed, _ := djangoHashPassword(plainPassword, 720000)
		a.db.Exec(`
			INSERT INTO auth_user (username, password, email, region, role, is_staff, is_active)
			VALUES (?, ?, '', '', 'class', 0, 1)
		`, classTeacher, hashed)
	}
	_, err := a.db.Exec(`
		INSERT INTO class_profile (school_id, class_number, class_letter, class_leader, class_teacher)
		VALUES (?, ?, ?, ?, ?)
	`, schoolID, classNumber, classLetter, classLeader, classTeacher)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
			WHERE class_id=? AND date(created_at) = date('now')
		`, c.ID).Scan(&videosToday)
		a.db.QueryRow(`
			SELECT COUNT(*) FROM lesson_stats 
			WHERE class_id=? AND date(created_at) >= date('now', '-30 days')
		`, c.ID).Scan(&videosMonth)
		a.db.QueryRow(`
			SELECT COUNT(*) FROM lesson_stats 
			WHERE class_id=? AND date(created_at) >= date('now', '-270 days')
		`, c.ID).Scan(&videosYear)

		// Dars jadvalini mapga olish
		// Kalitlar: 0=Yakshanba, 1=Dushanba, ..., 6=Shanba
		// Yakshanba jadvalga yozilmaydi, shuning uchun map'da yo'q → 0 qaytaradi
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
	rows, _ := a.db.Query(`
		SELECT u.id, u.username, u.email, u.region, u.role,
		       (SELECT COUNT(*) FROM user_progress WHERE user_id=u.id) as videos_watched,
		       (SELECT AVG(watched_percent) FROM user_progress WHERE user_id=u.id) as avg_percent
		FROM auth_user u WHERE u.role='user' AND u.is_staff=0 ORDER BY u.id DESC
	`)
	defer rows.Close()
	type StudentInfo struct {
		User
		VideosWatched int
		AvgPercent    int
	}
	var students []StudentInfo
	for rows.Next() {
		var s StudentInfo
		var avg sql.NullFloat64
		rows.Scan(&s.ID, &s.Username, &s.Email, &s.Region, &s.Role, &s.VideosWatched, &avg)
		if avg.Valid {
			s.AvgPercent = int(avg.Float64)
		}
		students = append(students, s)
	}
	a.render(w, r, "admin_users.html", map[string]any{"Students": students})
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