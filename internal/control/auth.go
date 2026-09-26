package control

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rpop-project/rpop/internal/store"
)

const (
	adminPasswordSetting      = "admin_password_hash"
	sessionCookieName         = "rpop_session"
	passwordIterations        = 600_000
	minimumPasswordCharacters = 12
	maximumPasswordBytes      = 1024
	maxLoginFailures          = 10
	loginFailureWindow        = 5 * time.Minute
	adminSessionDuration      = 12 * time.Hour
)

type loginAttempt struct {
	windowStart time.Time
	failures    int
}

type passwordRequest struct {
	Password string `json:"password"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (c *Control) authConfigured(r *http.Request) (bool, error) {
	_, err := c.store.GetSetting(r.Context(), adminPasswordSetting)
	if err == nil {
		return true, nil
	}
	if err == store.ErrSettingNotFound {
		return false, nil
	}
	return false, err
}

func (c *Control) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	configured, err := c.authConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not read authentication settings"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"configured":    configured,
		"authenticated": configured && c.sessionValid(r),
	})
}

func (c *Control) authSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	c.setupMu.Lock()
	defer c.setupMu.Unlock()
	configured, err := c.authConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not read authentication settings"})
		return
	}
	if configured {
		writeJSON(w, http.StatusConflict, apiError{"admin password is already configured"})
		return
	}
	var request passwordRequest
	if !decode(w, r, &request) {
		return
	}
	if err := validateAdminPassword(request.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}
	hash, err := hashAdminPassword(request.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not initialize admin password"})
		return
	}
	created, err := c.store.SetSettingIfAbsent(r.Context(), adminPasswordSetting, hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not initialize admin password"})
		return
	}
	if !created {
		writeJSON(w, http.StatusConflict, apiError{"admin password is already configured"})
		return
	}
	c.clearLoginFailures(r)
	if !c.issueSession(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (c *Control) authLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	if !c.loginAllowed(r) {
		writeJSON(w, http.StatusTooManyRequests, apiError{"too many authentication attempts; try again later"})
		return
	}
	storedHash, err := c.store.GetSetting(r.Context(), adminPasswordSetting)
	if err == store.ErrSettingNotFound {
		writeJSON(w, http.StatusPreconditionRequired, apiError{"set an admin password before logging in"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not read authentication settings"})
		return
	}
	var request passwordRequest
	if !decode(w, r, &request) {
		return
	}
	if !verifyAdminPassword(request.Password, string(storedHash)) {
		c.recordLoginFailure(r)
		writeJSON(w, http.StatusUnauthorized, apiError{"invalid password"})
		return
	}
	c.clearLoginFailures(r)
	if !c.issueSession(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (c *Control) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		c.authMu.Lock()
		delete(c.sessions, cookie.Value)
		c.authMu.Unlock()
	}
	c.expireSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (c *Control) authChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	var request passwordChangeRequest
	if !decode(w, r, &request) {
		return
	}
	if err := validateAdminPassword(request.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}
	storedHash, err := c.store.GetSetting(r.Context(), adminPasswordSetting)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not read authentication settings"})
		return
	}
	if !verifyAdminPassword(request.CurrentPassword, string(storedHash)) {
		writeJSON(w, http.StatusUnauthorized, apiError{"current password is incorrect"})
		return
	}
	hash, err := hashAdminPassword(request.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not change admin password"})
		return
	}
	if err := c.store.SetSetting(r.Context(), adminPasswordSetting, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not change admin password"})
		return
	}
	c.clearAllSessions()
	if !c.issueSession(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (c *Control) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAuthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		configured, err := c.authConfigured(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{"could not read authentication settings"})
			return
		}
		if !configured {
			writeJSON(w, http.StatusPreconditionRequired, apiError{"set an admin password using /api/auth/setup"})
			return
		}
		if !c.sessionValid(r) {
			writeJSON(w, http.StatusUnauthorized, apiError{"authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicAuthPath(path string) bool {
	switch path {
	case "/api/health", "/api/auth/status", "/api/auth/setup", "/api/auth/login", "/api/auth/logout":
		return true
	default:
		return false
	}
}

func (c *Control) issueSession(w http.ResponseWriter, r *http.Request) bool {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not create session"})
		return false
	}
	value := base64.RawURLEncoding.EncodeToString(token)
	expires := time.Now().Add(adminSessionDuration)
	c.authMu.Lock()
	for key, expiry := range c.sessions {
		if !expiry.After(time.Now()) {
			delete(c.sessions, key)
		}
	}
	c.sessions[value] = expires
	c.authMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/", Expires: expires,
		MaxAge: int(adminSessionDuration.Seconds()), HttpOnly: true, Secure: requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	return true
}

func (c *Control) expireSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (c *Control) sessionValid(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	now := time.Now()
	c.authMu.Lock()
	defer c.authMu.Unlock()
	expires, exists := c.sessions[cookie.Value]
	if !exists {
		return false
	}
	if !expires.After(now) {
		delete(c.sessions, cookie.Value)
		return false
	}
	return true
}

func (c *Control) clearAllSessions() {
	c.authMu.Lock()
	clear(c.sessions)
	c.authMu.Unlock()
}

func (c *Control) loginIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (c *Control) loginAllowed(r *http.Request) bool {
	ip := c.loginIP(r)
	now := time.Now()
	c.authMu.Lock()
	defer c.authMu.Unlock()
	for key, attempt := range c.loginAttempts {
		if now.Sub(attempt.windowStart) >= loginFailureWindow {
			delete(c.loginAttempts, key)
		}
	}
	attempt, ok := c.loginAttempts[ip]
	if !ok || now.Sub(attempt.windowStart) >= loginFailureWindow {
		return true
	}
	return attempt.failures < maxLoginFailures
}

func (c *Control) recordLoginFailure(r *http.Request) {
	ip := c.loginIP(r)
	now := time.Now()
	c.authMu.Lock()
	defer c.authMu.Unlock()
	attempt := c.loginAttempts[ip]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) >= loginFailureWindow {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.failures++
	c.loginAttempts[ip] = attempt
}

func (c *Control) clearLoginFailures(r *http.Request) {
	c.authMu.Lock()
	delete(c.loginAttempts, c.loginIP(r))
	c.authMu.Unlock()
}

func validateAdminPassword(password string) error {
	if utf8.RuneCountInString(password) < minimumPasswordCharacters {
		return fmt.Errorf("password must be at least %d characters", minimumPasswordCharacters)
	}
	if len(password) > maximumPasswordBytes {
		return fmt.Errorf("password must not exceed %d bytes", maximumPasswordBytes)
	}
	return nil
}

func hashAdminPassword(password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return nil, err
	}
	encoded := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived))
	return []byte(encoded), nil
}

func verifyAdminPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100_000 || iterations > 2_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != 32 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
