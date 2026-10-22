package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
	// AdminMode is an admin's switch for editing boards they aren't an editor
	// of. Off, they're treated like anyone else on those boards.
	AdminMode bool `json:"adminMode"`
}

// boardOverride reports whether u may act on any board as an admin right now.
func (u *User) boardOverride() bool { return u.IsAdmin && u.AdminMode }

const (
	sessionCookie = "gb_session"
	sessionTTL    = 30 * 24 * time.Hour
	bcryptCost    = 12
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// dummyHash is checked when a username doesn't exist, so response timing
// doesn't reveal which usernames are real.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcryptCost)

type authedHandler func(w http.ResponseWriter, r *http.Request, u *User)

func (a *App) auth(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.userFromRequest(w, r)
		if err != nil {
			serverErr(w, err)
			return
		}
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		h(w, r, u)
	}
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func (a *App) userFromRequest(w http.ResponseWriter, r *http.Request) (*User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	th := hashToken(c.Value)
	var u User
	var exp int64
	err = a.db.QueryRow(`SELECT u.id, u.username, u.is_admin, u.admin_mode, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, th).
		Scan(&u.ID, &u.Username, &u.IsAdmin, &u.AdminMode, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := time.Now()
	if exp < t.Unix() {
		_, err := a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, th)
		return nil, err
	}
	// Sliding expiry: extend once a session is past half its lifetime.
	if time.Unix(exp, 0).Sub(t) < sessionTTL/2 {
		newExp := t.Add(sessionTTL)
		if _, err := a.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, newExp.Unix(), th); err != nil {
			return nil, err
		}
		a.setSessionCookie(w, r, c.Value, newExp)
	}
	return &u, nil
}

func (a *App) secureCookies(r *http.Request) bool {
	switch a.cfg.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	return r.TLS != nil || (a.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (a *App) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(time.Until(exp).Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) createSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	exp := time.Now().Add(sessionTTL)
	if _, err := a.db.Exec(`INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now(), exp.Unix()); err != nil {
		return err
	}
	a.setSessionCookie(w, r, token, exp)
	return nil
}

func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		// The last hop is the one added by our own proxy; earlier ones are client-controlled.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	var admins int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&admins); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"signupsOpen": a.cfg.AllowSignups,
		"hasAdmin":    admins > 0,
	})
}

func validCredentials(w http.ResponseWriter, username, password string) bool {
	if !usernameRe.MatchString(username) {
		writeErr(w, http.StatusBadRequest, "username must be 3-32 characters: letters, numbers, . _ -")
		return false
	}
	if len(password) < 8 || len(password) > 72 {
		writeErr(w, http.StatusBadRequest, "password must be 8-72 characters")
		return false
	}
	return true
}

const maxPendingSignups = 25

// handleRegister files a sign-up request in pending_users. It creates no user
// and no session; an admin has to approve the request first.
func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.AllowSignups {
		writeErr(w, http.StatusForbidden, "sign-ups are closed")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Message  string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ipKey := "signup:" + a.clientIP(r)
	if !a.signups.allow(ipKey) {
		writeErr(w, http.StatusTooManyRequests, "too many sign-up requests from here, try again later")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !validCredentials(w, req.Username, req.Password) {
		return
	}
	msg, ok := cleanText(req.Message, 300)
	if !ok {
		writeErr(w, http.StatusBadRequest, "message must be 300 characters or less")
		return
	}

	var pending int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM pending_users`).Scan(&pending); err != nil {
		serverErr(w, err)
		return
	}
	if pending >= maxPendingSignups {
		writeErr(w, http.StatusServiceUnavailable, "too many sign-ups are waiting for approval right now, ask an admin")
		return
	}
	if taken, err := usernameTaken(a.db, req.Username); err != nil {
		serverErr(w, err)
		return
	} else if taken {
		writeErr(w, http.StatusConflict, "that username is taken")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		serverErr(w, err)
		return
	}
	if _, err := a.db.Exec(`INSERT INTO pending_users(username, password_hash, message, created_at) VALUES (?, ?, ?, ?)`,
		req.Username, string(hash), msg, now()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "that username is taken")
			return
		}
		serverErr(w, err)
		return
	}
	a.signups.fail(ipKey) // counts requests, successful or not
	writeJSON(w, http.StatusCreated, map[string]bool{"pending": true})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	ipKey := "ip:" + a.clientIP(r)
	userKey := "user:" + strings.ToLower(username)
	if !a.limiter.allow(ipKey) || !a.limiter.allow(userKey) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again in a few minutes")
		return
	}

	var u User
	var hash string
	err := a.db.QueryRow(`SELECT id, username, is_admin, admin_mode, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.IsAdmin, &u.AdminMode, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverErr(w, err)
		return
	}
	ok := false
	if err == nil {
		ok = bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
	} else {
		// Not a user. If it's a pending sign-up with the right password, say
		// so; otherwise fall through to the generic failure.
		var pendingHash string
		perr := a.db.QueryRow(`SELECT password_hash FROM pending_users WHERE username = ?`, username).Scan(&pendingHash)
		if perr != nil && !errors.Is(perr, sql.ErrNoRows) {
			serverErr(w, perr)
			return
		}
		if perr == nil {
			if bcrypt.CompareHashAndPassword([]byte(pendingHash), []byte(req.Password)) == nil {
				writeErr(w, http.StatusForbidden, "your account is still waiting for an admin to approve it")
				return
			}
		} else {
			bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		}
	}
	if !ok {
		a.limiter.fail(ipKey)
		a.limiter.fail(userKey)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	a.limiter.reset(userKey)
	if _, err := a.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now(), u.ID); err != nil {
		serverErr(w, err)
		return
	}
	if err := a.createSession(w, r, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeMe(w, http.StatusOK, &u)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value)); err != nil {
			serverErr(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request, u *User) {
	a.writeMe(w, http.StatusOK, u)
}

// handleAdminMode switches an admin between view and edit mode on other people's boards.
func (a *App) handleAdminMode(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, err := a.db.Exec(`UPDATE users SET admin_mode = ? WHERE id = ?`, req.On, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	u.AdminMode = req.On
	a.writeMe(w, http.StatusOK, u)
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	userKey := "user:" + strings.ToLower(u.Username)
	if !a.limiter.allow(userKey) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	if len(req.New) < 8 || len(req.New) > 72 {
		writeErr(w, http.StatusBadRequest, "new password must be 8-72 characters")
		return
	}
	var hash string
	if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		serverErr(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) != nil {
		a.limiter.fail(userKey)
		writeErr(w, http.StatusForbidden, "current password is wrong")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcryptCost)
	if err != nil {
		serverErr(w, err)
		return
	}
	if _, err := a.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(newHash), u.ID); err != nil {
		serverErr(w, err)
		return
	}
	// Log out every other session for this user.
	c, _ := r.Cookie(sessionCookie)
	if _, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, u.ID, hashToken(c.Value)); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// limiter counts failures per key inside a fixed window.
type limiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
	max     int
	window  time.Duration
}

type limitEntry struct {
	count int
	start time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{entries: map[string]*limitEntry{}, max: max, window: window}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return true
	}
	if time.Since(e.start) > l.window {
		delete(l.entries, key)
		return true
	}
	return e.count < l.max
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || time.Since(e.start) > l.window {
		e = &limitEntry{start: time.Now()}
		l.entries[key] = e
	}
	e.count++
	if len(l.entries) > 10000 {
		for k, v := range l.entries {
			if time.Since(v.start) > l.window {
				delete(l.entries, k)
			}
		}
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}
