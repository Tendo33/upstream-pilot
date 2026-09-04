package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie   = "s2am_session"
	csrfCookie      = "s2am_csrf"
	userAdminLockID = int64(7820133777)

	credentialFailureLimit = 5
	clientFailureLimit     = 30
	passwordHashPrefix     = "$s2am-sha256$"
	dummyPasswordHash      = passwordHashPrefix + "$2a$12$QJWdPvDjdwWG7q8AAMvR0evO2hXloZGNKMVm/2rZ4nVB/Hk4VQf.q"
)

type credentialsInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 {
		return "", &apiError{Status: http.StatusBadRequest, Code: "INVALID_EMAIL", Message: "请输入有效的邮箱地址"}
	}
	return value, nil
}

func validatePassword(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 10 || length > 128 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PASSWORD", Message: "密码长度必须为 10 到 128 个字符"}
	}
	return nil
}

func passwordMaterial(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return []byte(base64.RawStdEncoding.EncodeToString(digest[:]))
}

func hashPassword(value string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword(passwordMaterial(value), 12)
	if err != nil {
		return "", err
	}
	return passwordHashPrefix + string(hash), nil
}

func verifyPassword(hash, value string) error {
	if strings.HasPrefix(hash, passwordHashPrefix) {
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(hash, passwordHashPrefix)), passwordMaterial(value))
	}
	// Passwords created by development builds before pre-hashing remain valid.
	if len(value) > 72 {
		_ = bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(dummyPasswordHash, passwordHashPrefix)), passwordMaterial(value))
		return bcrypt.ErrMismatchedHashAndPassword
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(value))
}

func (a *App) setupStatus(w http.ResponseWriter, r *http.Request) error {
	var initialized bool
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&initialized); err != nil {
		return err
	}
	writeData(w, http.StatusOK, map[string]bool{"initialized": initialized})
	return nil
}

func (a *App) setup(w http.ResponseWriter, r *http.Request) error {
	var input credentialsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return err
	}
	if err := validatePassword(input.Password); err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock($1)`, userAdminLockID); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return &apiError{Status: http.StatusConflict, Code: "ALREADY_INITIALIZED", Message: "系统已经完成初始化"}
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		return err
	}
	user := User{ID: uuid.NewString(), Email: email, Role: "admin", Enabled: true, CreatedAt: time.Now()}
	if _, err := tx.Exec(r.Context(), `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,'admin')`, user.ID, email, hash); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	if err := a.issueSession(w, r.Context(), user); err != nil {
		return err
	}
	writeData(w, http.StatusCreated, user)
	return nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var input credentialsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return &apiError{Status: http.StatusUnauthorized, Code: "INVALID_CREDENTIALS", Message: "邮箱或密码错误"}
	}
	ip := clientIP(r)
	credentialThrottleKey := a.authThrottleKey("credential", email+"\x00"+ip)
	clientThrottleKey := a.authThrottleKey("client", ip)
	blocked, err := a.loginBlocked(r.Context(), credentialThrottleKey, clientThrottleKey)
	if err != nil {
		return err
	}
	if blocked {
		return &apiError{Status: http.StatusTooManyRequests, Code: "LOGIN_RATE_LIMITED", Message: "登录尝试过于频繁，请稍后重试"}
	}
	var user User
	var passwordHash string
	err = a.db.QueryRow(r.Context(), `SELECT id,email,password_hash,role,enabled,created_at,last_login_at FROM users WHERE lower(email)=lower($1)`, email).Scan(
		&user.ID, &user.Email, &passwordHash, &user.Role, &user.Enabled, &user.CreatedAt, &user.LastLoginAt,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	passwordMatches := false
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep failure timing less distinguishable for unknown users.
		_ = verifyPassword(dummyPasswordHash, input.Password)
	} else {
		passwordMatches = verifyPassword(passwordHash, input.Password) == nil
	}
	if !passwordMatches {
		if err := a.recordLoginFailure(r.Context(), credentialThrottleKey, credentialFailureLimit); err != nil {
			return err
		}
		if err := a.recordLoginFailure(r.Context(), clientThrottleKey, clientFailureLimit); err != nil {
			return err
		}
		return &apiError{Status: http.StatusUnauthorized, Code: "INVALID_CREDENTIALS", Message: "邮箱或密码错误"}
	}
	if !user.Enabled {
		return &apiError{Status: http.StatusForbidden, Code: "USER_DISABLED", Message: "该用户已被停用"}
	}
	if err := a.issueLoginSession(w, r.Context(), user, passwordHash); err != nil {
		return err
	}
	_, _ = a.db.Exec(r.Context(), `DELETE FROM auth_throttles WHERE key_hash=$1`, credentialThrottleKey)
	writeData(w, http.StatusOK, user)
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (a *App) authThrottleKey(scope, value string) []byte {
	mac := hmac.New(sha256.New, a.config.MasterKey)
	_, _ = mac.Write([]byte(scope + "\x00" + strings.ToLower(value)))
	return mac.Sum(nil)
}

func (a *App) loginBlocked(ctx context.Context, keys ...[]byte) (bool, error) {
	for _, key := range keys {
		var blocked bool
		err := a.db.QueryRow(ctx, `SELECT COALESCE(blocked_until>now(),false) FROM auth_throttles WHERE key_hash=$1`, key).Scan(&blocked)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if blocked {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) recordLoginFailure(ctx context.Context, key []byte, limit int) error {
	_, err := a.db.Exec(ctx, `
		INSERT INTO auth_throttles(key_hash,failures,window_started_at,blocked_until,updated_at)
		VALUES($1,1,now(),NULL,now())
		ON CONFLICT(key_hash) DO UPDATE SET
		 failures=CASE WHEN auth_throttles.window_started_at<now()-interval '15 minutes' THEN 1 ELSE auth_throttles.failures+1 END,
		 window_started_at=CASE WHEN auth_throttles.window_started_at<now()-interval '15 minutes' THEN now() ELSE auth_throttles.window_started_at END,
		 blocked_until=CASE
		   WHEN (CASE WHEN auth_throttles.window_started_at<now()-interval '15 minutes' THEN 1 ELSE auth_throttles.failures+1 END)>=$2
		   THEN now()+interval '15 minutes' ELSE NULL END,
		 updated_at=now()`, key, limit)
	return err
}

func randomToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func (a *App) issueSession(w http.ResponseWriter, ctx context.Context, user User) error {
	token, tokenHash, err := randomToken()
	if err != nil {
		return err
	}
	csrf, csrfHash, err := randomToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(a.config.SessionTTL)
	if _, err := a.db.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), user.ID, tokenHash, csrfHash, expires); err != nil {
		return err
	}
	a.setSessionCookies(w, token, csrf, expires)
	return nil
}

func (a *App) issueLoginSession(w http.ResponseWriter, ctx context.Context, user User, expectedPasswordHash string) error {
	token, tokenHash, err := randomToken()
	if err != nil {
		return err
	}
	csrf, csrfHash, err := randomToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(a.config.SessionTTL)
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentPasswordHash string
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT password_hash,enabled FROM users WHERE id=$1 FOR UPDATE`, user.ID).Scan(&currentPasswordHash, &enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare([]byte(currentPasswordHash), []byte(expectedPasswordHash)) != 1) {
		return &apiError{Status: http.StatusUnauthorized, Code: "INVALID_CREDENTIALS", Message: "邮箱或密码错误"}
	}
	if err != nil {
		return err
	}
	if !enabled {
		return &apiError{Status: http.StatusForbidden, Code: "USER_DISABLED", Message: "该用户已被停用"}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), user.ID, tokenHash, csrfHash, expires); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, user.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	a.setSessionCookies(w, token, csrf, expires)
	return nil
}

func (a *App) setSessionCookies(w http.ResponseWriter, token, csrf string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.config.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(a.config.SessionTTL.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: a.config.CookieSecure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(a.config.SessionTTL.Seconds())})
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookie, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录")
			return
		}
		hash := sha256.Sum256([]byte(cookie.Value))
		var identity Identity
		err = a.db.QueryRow(r.Context(), `
			SELECT u.id,u.email,u.role,u.enabled,u.created_at,u.last_login_at,s.id,s.csrf_hash
			FROM sessions s JOIN users u ON u.id=s.user_id
			WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled=true`, hash[:]).Scan(
			&identity.ID, &identity.Email, &identity.Role, &identity.Enabled, &identity.CreatedAt, &identity.LastLoginAt, &identity.SessionID, &identity.CSRFHash,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			clearSessionCookies(w, a.config.CookieSecure)
			writeError(w, http.StatusUnauthorized, "SESSION_EXPIRED", "登录状态已失效")
			return
		}
		if err != nil {
			a.logger.Error("session lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务器处理请求失败")
			return
		}
		_, _ = a.db.Exec(r.Context(), `UPDATE sessions SET last_seen_at=now() WHERE id=$1 AND last_seen_at<now()-interval '10 minutes'`, identity.SessionID)
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
	})
}

func (a *App) verifyCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		supplied := r.Header.Get("X-CSRF-Token")
		hash := sha256.Sum256([]byte(supplied))
		identity := identityFrom(r)
		if supplied == "" || subtle.ConstantTimeCompare(hash[:], identity.CSRFHash) != 1 {
			writeError(w, http.StatusForbidden, "CSRF_INVALID", "请求校验失败，请刷新页面后重试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	writeData(w, http.StatusOK, identityFrom(r).User)
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if _, err := a.db.Exec(r.Context(), `DELETE FROM sessions WHERE id=$1`, identity.SessionID); err != nil {
		return err
	}
	clearSessionCookies(w, a.config.CookieSecure)
	writeData(w, http.StatusOK, map[string]bool{"logged_out": true})
	return nil
}

func requireAdmin(identity Identity) error {
	if identity.Role != "admin" {
		return &apiError{Status: http.StatusForbidden, Code: "ADMIN_REQUIRED", Message: "此操作仅限管理员"}
	}
	return nil
}
