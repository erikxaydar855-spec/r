package main

import (
    "crypto/hmac"
    "crypto/rand"
    "crypto/sha256"
    "crypto/subtle"
    "crypto/tls"
    "encoding/hex"
    "fmt"
    "net"
    "net/http"
    "net/mail"
    "net/smtp"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/go-chi/chi/v5"
)

// ITUBE_ACCOUNT_V1: password recovery is allowed only to verified email.
func ensureAccountSchema(db *DB) error {
    queries := []string{
        `ALTER TABLE auth_user ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE`,
        `ALTER TABLE auth_user ADD COLUMN IF NOT EXISTS session_version INTEGER NOT NULL DEFAULT 1`,
        `CREATE TABLE IF NOT EXISTS account_codes (
             user_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
             purpose TEXT NOT NULL CHECK(purpose IN ('verify','reset')),
             email TEXT NOT NULL,
             code_hash TEXT NOT NULL,
             expires_at TIMESTAMPTZ NOT NULL,
             last_sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
             window_start TIMESTAMPTZ NOT NULL DEFAULT NOW(),
             send_count INTEGER NOT NULL DEFAULT 1,
             attempts INTEGER NOT NULL DEFAULT 0,
             PRIMARY KEY(user_id,purpose)
         )`,
    }
    for _, q := range queries {
        if _, err := db.Exec(q); err != nil { return fmt.Errorf("account schema: %w",err) }
    }
    return nil
}

func (a *App) accountGuard(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/media/") {
            next.ServeHTTP(w,r); return
        }
        session, _ := a.store.Get(r,"itube_session")
        if id, ok := session.Values["user_id"].(int); ok && id>0 {
            var version, active int
            err := a.db.QueryRow(`SELECT session_version,is_active FROM auth_user WHERE id=?`,id).Scan(&version,&active)
            saved, _ := session.Values["account_version"].(int)
            if err!=nil || active!=1 || saved!=version {
                session.Values = make(map[interface{}]interface{})
                _ = session.Save(r,w)
            }
        }
        next.ServeHTTP(w,r)
    })
}
func (a *App) stampAccountSession(userID int, sessionVersion *int) {
    ver := 1
    _ = a.db.QueryRow(`SELECT session_version FROM auth_user WHERE id=?`, userID).Scan(&ver)
    *sessionVersion = ver
}

func accountEmail(raw string) (string,bool) {
    e:=strings.ToLower(strings.TrimSpace(raw))
    if len(e)>254 || len(e)<6 || strings.ContainsAny(e,"\r\n") { return "",false }
    addr,err:=mail.ParseAddress(e)
    return e,err==nil && strings.EqualFold(addr.Address,e)
}
func accountRegion(raw string) (string,bool) {
    region:=strings.TrimSpace(raw)
    switch region {
    case "Toshkent", "Toshkent shahri", "Samarqand", "Buxoro", "Navoiy", "Farg'ona", "Namangan", "Andijon", "Qashqadaryo", "Surxondaryo", "Xorazm", "Qoraqalpog'iston", "Sirdaryo", "Jizzax":
        return region,true
    default: return "",false
    }
}
func accountCodeHash(userID int,purpose,code string) (string,error) {
    key:=os.Getenv("ITUBE_RESET_SECRET")
    if len(key)<32 { key=os.Getenv("SESSION_SECRET") }
    if len(key)<32 { return "",fmt.Errorf("ITUBE_RESET_SECRET or SESSION_SECRET must contain >=32 characters") }
    h:=hmac.New(sha256.New, []byte(key))
    fmt.Fprintf(h,"%d:%s:%s",userID,purpose,code)
    return hex.EncodeToString(h.Sum(nil)),nil
}
func smtpSendCode(to, code, purpose string) error {
    host:=strings.TrimSpace(os.Getenv("ITUBE_SMTP_HOST"))
    port:=strings.TrimSpace(os.Getenv("ITUBE_SMTP_PORT"))
    if port=="" { port="587" }
    user:=os.Getenv("ITUBE_SMTP_USER")
    pass:=os.Getenv("ITUBE_SMTP_PASSWORD")
    from:=strings.TrimSpace(os.Getenv("ITUBE_SMTP_FROM"))
    if host=="" || user=="" || pass=="" || from=="" { return fmt.Errorf("SMTP configuration is missing") }
    fromEmail, valid:=accountEmail(from)
    if !valid { return fmt.Errorf("SMTP FROM is not valid") }
    if _,valid=accountEmail(to); !valid { return fmt.Errorf("recipient is invalid") }
    conn,err:=net.DialTimeout("tcp",net.JoinHostPort(host,port),8*time.Second)
    if err!=nil { return err }
    defer conn.Close()
    _=conn.SetDeadline(time.Now().Add(18*time.Second))
    client,err:=smtp.NewClient(conn,host)
    if err!=nil { return err }
    defer client.Close()
    if ok,_:=client.Extension("STARTTLS"); !ok { return fmt.Errorf("SMTP requires STARTTLS") }
    if err=client.StartTLS(&tls.Config{ServerName:host,MinVersion:tls.VersionTLS12}); err!=nil {return err}
    if err=client.Auth(smtp.PlainAuth("",user,pass,host)); err!=nil {return err}
    if err=client.Mail(fromEmail); err!=nil {return err}
    if err=client.Rcpt(to); err!=nil {return err}
    writer,err:=client.Data(); if err!=nil {return err}
    subject:="ITube email confirmation"
    if purpose=="reset" { subject="ITube password reset code" }
    msg:=fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nITube tasdiqlash kodi: %s\r\nKod 10 daqiqa amal qiladi. Uni hech kimga bermang.\r\n",fromEmail,to,subject,code)
    _,err=writer.Write([]byte(msg))
    if closeErr:=writer.Close(); err==nil {err=closeErr}
    if err!=nil {return err}
    return client.Quit()
}

func (a *App) accountSendCode(userID int,purpose,email string) error {
    if os.Getenv("ITUBE_RESEND_API_KEY")=="" || os.Getenv("ITUBE_RESEND_FROM")=="" {
        return fmt.Errorf("Resend API not configured")
    }
    b:=make([]byte,4)
    if _,err:=rand.Read(b); err!=nil {return err}
    num:=(uint64(b[0])<<24)|(uint64(b[1])<<16)|(uint64(b[2])<<8)|uint64(b[3])
    code:=fmt.Sprintf("%06d",num%1000000)
    hash,err:=accountCodeHash(userID,purpose,code)
    if err!=nil {return err}
    result,err:=a.db.Exec(`INSERT INTO account_codes(user_id,purpose,email,code_hash,expires_at)
        VALUES(?,?,?,?,NOW()+INTERVAL '10 minutes')
        ON CONFLICT (user_id,purpose) DO UPDATE SET
          email=EXCLUDED.email,code_hash=EXCLUDED.code_hash,
          expires_at=EXCLUDED.expires_at,last_sent_at=NOW(),attempts=0,
          send_count=CASE WHEN account_codes.window_start < NOW()-INTERVAL '1 hour' THEN 1 ELSE account_codes.send_count+1 END,
          window_start=CASE WHEN account_codes.window_start < NOW()-INTERVAL '1 hour' THEN NOW() ELSE account_codes.window_start END
        WHERE account_codes.last_sent_at < NOW()-INTERVAL '90 seconds'
          AND (account_codes.window_start < NOW()-INTERVAL '1 hour' OR account_codes.send_count < 5)`,userID,purpose,email,hash)
    if err!=nil {return err}
    count,_:=result.RowsAffected()
    if count==0 {return fmt.Errorf("email cooldown or hourly limit")}
    if err=resendSendCode(email,code,purpose); err!=nil {
        // Remove failed delivery token, do not retain an unusable code.
        _,_=a.db.Exec(`DELETE FROM account_codes WHERE user_id=? AND purpose=? AND code_hash=?`,userID,purpose,hash)
        return err
    }
    return nil
}

func (a *App) accountCheckCode(userID int,purpose,email,code,newPassword string) bool {
    if len(code)!=6 {return false}
    for _,c:=range code {if c<'0'||c>'9' {return false}}
    hash,err:=accountCodeHash(userID,purpose,code); if err!=nil{return false}
    tx,err:=a.db.DB.Begin(); if err!=nil{return false}
    defer tx.Rollback()
    var expected, savedEmail string
    var expires time.Time
    var attempts int
    err=tx.QueryRow(`SELECT code_hash,email,expires_at,attempts FROM account_codes WHERE user_id=$1 AND purpose=$2 FOR UPDATE`,userID,purpose).Scan(&expected,&savedEmail,&expires,&attempts)
    if err!=nil || attempts>=5 || !time.Now().Before(expires) || !strings.EqualFold(savedEmail,email) { return false }
    if subtle.ConstantTimeCompare([]byte(expected),[]byte(hash))!=1 {
        _,_ = tx.Exec(`UPDATE account_codes SET attempts=attempts+1 WHERE user_id=$1 AND purpose=$2`,userID,purpose)
        _=tx.Commit()
        return false
    }
    if purpose=="verify" {
        result,e:=tx.Exec(`UPDATE auth_user SET email_verified=TRUE WHERE id=$1 AND lower(email)=lower($2)`,userID,email)
        if e!=nil {return false}
        count,_:=result.RowsAffected(); if count!=1{return false}
    } else if purpose=="reset" {
        if len([]rune(newPassword))<12 || len(newPassword)>256 {return false}
        var isStaff int
        if e:=tx.QueryRow(`SELECT is_staff FROM auth_user WHERE id=$1`,userID).Scan(&isStaff); e!=nil {return false}
        if isStaff==1 && len([]rune(newPassword))<16 {return false}
        newHash,e:=djangoHashPassword(newPassword,720000); if e!=nil{return false}
        if _,e=tx.Exec(`UPDATE auth_user SET password=$1,session_version=session_version+1 WHERE id=$2 AND is_active=1`,newHash,userID); e!=nil {return false}
    } else {return false}
    _,err=tx.Exec(`DELETE FROM account_codes WHERE user_id=$1 AND purpose=$2`,userID,purpose)
    return err==nil && tx.Commit()==nil
}

func (a *App) accountContactHandler(w http.ResponseWriter,r *http.Request) {
    userID,ok:=a.currentUserID(r);if !ok {http.Redirect(w,r,"/auth",303);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    email,ok:=accountEmail(r.FormValue("email"));if !ok {http.Error(w,"Email xato",400);return}
    region,ok:=accountRegion(r.FormValue("region")); if !ok {http.Error(w,"Viloyatni tanlang",400);return}
    // Class identity is managed by its school, not class pupils.
    if a.currentUserRole(r)=="class" {http.Error(w,"Sinf emaili maktab orqali boshqariladi",403);return}
    var oldHash string
    if err:=a.db.QueryRow(`SELECT password FROM auth_user WHERE id=?`,userID).Scan(&oldHash); err!=nil || !djangoCheckPassword(oldHash,r.FormValue("current_password")) {
        http.Error(w,"Emailni ozgartirish uchun joriy parolni togri kiriting",403);return
    }
    _,err:=a.db.Exec(`UPDATE auth_user SET email_verified=CASE WHEN lower(email)=lower(?) THEN email_verified ELSE FALSE END,email=?,region=? WHERE id=?`,email,email,region,userID)
    if err!=nil {http.Error(w,"Saqlanmadi",500);return}
    _,_=a.db.Exec(`DELETE FROM account_codes WHERE user_id=?`,userID)
    http.Redirect(w,r,"/profile?notice=saved",303)
}
func (a *App) accountSendVerifyHandler(w http.ResponseWriter,r *http.Request) {
    userID,ok:=a.currentUserID(r);if !ok {http.Redirect(w,r,"/auth",303);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    if a.currentUserRole(r)=="class" {http.Error(w,"not allowed",403);return}
    var email string;var verified bool
    if err:=a.db.QueryRow(`SELECT email,email_verified FROM auth_user WHERE id=?`,userID).Scan(&email,&verified);err!=nil {http.Error(w,"error",500);return}
    if verified {http.Redirect(w,r,"/profile?notice=verified",303);return}
    if _,valid:=accountEmail(email);!valid {http.Redirect(w,r,"/profile?notice=need-email",303);return}
    if err:=a.accountSendCode(userID,"verify",email);err!=nil {http.Redirect(w,r,"/profile?notice=mail-error",303);return}
    http.Redirect(w,r,"/profile?notice=sent",303)
}
func (a *App) accountVerifyHandler(w http.ResponseWriter,r *http.Request) {
    userID,ok:=a.currentUserID(r);if !ok {http.Redirect(w,r,"/auth",303);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    var email string
    _=a.db.QueryRow(`SELECT email FROM auth_user WHERE id=?`,userID).Scan(&email)
    if a.accountCheckCode(userID,"verify",email,strings.TrimSpace(r.FormValue("code")),"") {
        http.Redirect(w,r,"/profile?notice=verified",303)
    } else {http.Redirect(w,r,"/profile?notice=wrong-code",303)}
}
func (a *App) accountRecoverPage(w http.ResponseWriter,r *http.Request) {
    a.render(w,r,"account_recover.html",map[string]any{"Success":r.URL.Query().Get("sent")=="1"})
}
func (a *App) accountRecoverRequest(w http.ResponseWriter,r *http.Request) {
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    username:=strings.TrimSpace(r.FormValue("username"))
    email,valid:=accountEmail(r.FormValue("email"))
    if valid && len(username)>0 && len(username)<128 {
        var userID int
        err:=a.db.QueryRow(`SELECT id FROM auth_user WHERE username=? AND lower(email)=? AND email_verified=TRUE AND is_active=1 AND role!='class'`,username,email).Scan(&userID)
        if err==nil {_=a.accountSendCode(userID,"reset",email)}
    }
    // Same answer for known and unknown identities.
    http.Redirect(w,r,"/account/recover?sent=1",303)
}
func (a *App) accountRecoverConfirm(w http.ResponseWriter,r *http.Request) {
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    username:=strings.TrimSpace(r.FormValue("username"))
    email,valid:=accountEmail(r.FormValue("email"))
    code:=strings.TrimSpace(r.FormValue("code"))
    password:=r.FormValue("new_password")
    if !valid || username=="" || len([]rune(password))<12 || password!=r.FormValue("confirm_password") {
        a.render(w,r,"account_recover.html",map[string]any{"Error":"Ma'lumotlarni to'liq kiriting. Parol 12+ belgi bo'lsin."});return
    }
    var userID int
    err:=a.db.QueryRow(`SELECT id FROM auth_user WHERE username=? AND lower(email)=? AND email_verified=TRUE AND is_active=1 AND role!='class'`,username,email).Scan(&userID)
    if err!=nil || !a.accountCheckCode(userID,"reset",email,code,password) {
        a.render(w,r,"account_recover.html",map[string]any{"Error":"Kod noto'g'ri, eskirgan yoki yangi parol talabga mos emas."});return
    }
    http.Redirect(w,r,"/auth?reset=ok",303)
}

// School can manage only classes/students that belong to its school.
func (a *App) classEditorAllowed(r *http.Request,classID int) bool {
    id,ok:=a.currentUserID(r);if !ok || classID<1 {return false}
    var count int
    err:=a.db.QueryRow(`SELECT COUNT(*) FROM class_profile c JOIN school_profile s ON s.id=c.school_id JOIN auth_user u ON u.id=? AND u.is_active=1
      WHERE c.id=? AND (u.is_staff=1 OR (u.role='school' AND s.user_id=u.id))`,id,classID).Scan(&count)
    return err==nil && count==1
}
func (a *App) classEditorPage(w http.ResponseWriter,r *http.Request) {
    userID,ok:=a.currentUserID(r);if !ok {http.Redirect(w,r,"/auth",303);return}
    isStaff:=a.currentUserIsStaff(r)
    if !isStaff && a.currentUserRole(r)!="school" {http.Error(w,"not allowed",403);return}
    query:=`SELECT c.id,c.school_id,s.school_name,c.class_number,c.class_letter,c.class_leader,c.class_teacher
        FROM class_profile c JOIN school_profile s ON s.id=c.school_id WHERE 1=1`
    args:=[]any{}
    if !isStaff {query+=` AND s.user_id=?`;args=append(args,userID)}
    if isStaff && r.URL.Query().Get("school_id")!="" {
        sid,err:=strconv.Atoi(r.URL.Query().Get("school_id"));if err!=nil || sid<1 {http.Error(w,"bad school",400);return}
        query+=` AND s.id=?`;args=append(args,sid)
    }
    query+=` ORDER BY s.school_name,c.class_number,c.class_letter`
    rows,err:=a.db.Query(query,args...)
    if err!=nil {http.Error(w,"server error",500);return}
    type studentView struct {ID int; FullName string; BirthYear int;Gender string;Address string;Origin string}
    type classView struct {ID int;SchoolID int;SchoolName string;ClassNumber int;ClassLetter string;ClassLeader string;ClassTeacher string;Students []studentView}
    out:=[]classView{}
    for rows.Next() {
        var c classView
        if err=rows.Scan(&c.ID,&c.SchoolID,&c.SchoolName,&c.ClassNumber,&c.ClassLetter,&c.ClassLeader,&c.ClassTeacher);err!=nil {rows.Close();http.Error(w,"query error",500);return}
        out=append(out,c)
    }
    rows.Close()
    for i:=range out {
        srows,err:=a.db.Query(`SELECT id,full_name,COALESCE(birth_year,0),COALESCE(gender,''),COALESCE(address,''),COALESCE(origin,'') FROM students WHERE class_id=? ORDER BY full_name`,out[i].ID)
        if err!=nil {http.Error(w,"query error",500);return}
        out[i].Students=[]studentView{}
        for srows.Next(){var s studentView;if err=srows.Scan(&s.ID,&s.FullName,&s.BirthYear,&s.Gender,&s.Address,&s.Origin);err!=nil {srows.Close();http.Error(w,"query error",500);return};out[i].Students=append(out[i].Students,s)}
        srows.Close()
    }
    a.render(w,r,"class_editor.html",map[string]any{"Classes":out,"IsAdmin":isStaff})
}
func (a *App) classEditorUpdate(w http.ResponseWriter,r *http.Request) {
    classID,_:=strconv.Atoi(chi.URLParam(r,"id"))
    if !a.classEditorAllowed(r,classID) {http.Error(w,"forbidden",403);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    number,e:=strconv.Atoi(r.FormValue("class_number"))
    letter:=strings.ToUpper(strings.TrimSpace(r.FormValue("class_letter")))
    teacher:=strings.TrimSpace(r.FormValue("class_teacher"));leader:=strings.TrimSpace(r.FormValue("class_leader"))
    password:=r.FormValue("class_password")
    if e!=nil || number<1 || number>11 || len([]rune(letter))>3 || letter=="" || teacher=="" || leader=="" || len([]rune(teacher))>120 || len([]rune(leader))>120 {
       http.Error(w,"invalid data",400);return
    }
    var hash string
    if password!="" {
        if len([]rune(password))<12 || len(password)>256 {http.Error(w,"Parol 12+ belgi",400);return}
        hash,e=djangoHashPassword(password,720000);if e!=nil {http.Error(w,"hash",500);return}
    }
    tx,e:=a.db.DB.Begin();if e!=nil {http.Error(w,"db",500);return};defer tx.Rollback()
    _,e=tx.Exec(`UPDATE class_profile SET class_number=$1,class_letter=$2,class_teacher=$3,class_leader=$4 WHERE id=$5`,number,letter,teacher,leader,classID)
    if e!=nil {http.Error(w,"Sinf raqami yoki harfi band",409);return}
    if hash!="" {
        res,e:=tx.Exec(`UPDATE auth_user SET password=$1,session_version=session_version+1 WHERE username=$2 AND role='class'`,hash,fmt.Sprintf("class_%d",classID))
        if e!=nil {http.Error(w,"Sinf parolini saqlash xatosi",500);return}
        count,_:=res.RowsAffected();if count!=1 {http.Error(w,"Sinf hisobi topilmadi",500);return}
    }
    if e=tx.Commit();e!=nil {http.Error(w,"db",500);return}
    http.Redirect(w,r,"/school/classes#class-"+strconv.Itoa(classID),303)
}
func (a *App) classEditorStudentAdd(w http.ResponseWriter,r *http.Request) {
    classID,_:=strconv.Atoi(chi.URLParam(r,"id"))
    if !a.classEditorAllowed(r,classID) {http.Error(w,"forbidden",403);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    name:=strings.TrimSpace(r.FormValue("full_name"));if len([]rune(name))<2 || len([]rune(name))>150 {http.Error(w,"invalid name",400);return}
    birth,_:=strconv.Atoi(r.FormValue("birth_year"));if birth<1900 || birth>time.Now().Year() {birth=0}
    _,err:=a.db.Exec(`INSERT INTO students(class_id,full_name,birth_year,gender,address,origin) VALUES(?,?,?,?,?,?)`,classID,name,birth,strings.TrimSpace(r.FormValue("gender")),strings.TrimSpace(r.FormValue("address")),strings.TrimSpace(r.FormValue("origin")))
    if err!=nil {http.Error(w,"db",500);return}
    http.Redirect(w,r,"/school/classes#class-"+strconv.Itoa(classID),303)
}
func (a *App) classEditorStudentUpdate(w http.ResponseWriter,r *http.Request) {
    classID,_:=strconv.Atoi(chi.URLParam(r,"id"))
    sid,_:=strconv.Atoi(chi.URLParam(r,"studentID"))
    if !a.classEditorAllowed(r,classID) {http.Error(w,"forbidden",403);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    name:=strings.TrimSpace(r.FormValue("full_name"));if len([]rune(name))<2||len([]rune(name))>150 {http.Error(w,"invalid name",400);return}
    birth,_:=strconv.Atoi(r.FormValue("birth_year"));if birth<1900||birth>time.Now().Year(){birth=0}
    res,err:=a.db.Exec(`UPDATE students SET full_name=?,birth_year=?,gender=?,address=?,origin=? WHERE id=? AND class_id=?`,name,birth,strings.TrimSpace(r.FormValue("gender")),strings.TrimSpace(r.FormValue("address")),strings.TrimSpace(r.FormValue("origin")),sid,classID)
    if err!=nil {http.Error(w,"db",500);return}
    affected,_:=res.RowsAffected();if affected!=1 {http.NotFound(w,r);return}
    http.Redirect(w,r,"/school/classes#class-"+strconv.Itoa(classID),303)
}
func (a *App) classEditorStudentDelete(w http.ResponseWriter,r *http.Request) {
    classID,_:=strconv.Atoi(chi.URLParam(r,"id"))
    sid,_:=strconv.Atoi(chi.URLParam(r,"studentID"))
    if !a.classEditorAllowed(r,classID) {http.Error(w,"forbidden",403);return}
    if !a.verifyCSRFToken(r) {http.Error(w,"csrf",403);return}
    _,err:=a.db.Exec(`DELETE FROM students WHERE id=? AND class_id=?`,sid,classID)
    if err!=nil {http.Error(w,"db",500);return}
    http.Redirect(w,r,"/school/classes#class-"+strconv.Itoa(classID),303)
}
