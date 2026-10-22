package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Admin accounts can only be created or granted from the server CLI
// (gameboard create-admin / make-admin). The web admin page manages regular
// users: approving sign-ups, creating accounts, resets and removals.

func (a *App) admin(h authedHandler) http.HandlerFunc {
	return a.auth(func(w http.ResponseWriter, r *http.Request, u *User) {
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admins only")
			return
		}
		h(w, r, u)
	})
}

type PendingUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"createdAt"`
}

type AdminUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	IsAdmin     bool   `json:"isAdmin"`
	CreatedAt   int64  `json:"createdAt"`
	LastLoginAt int64  `json:"lastLoginAt"`
}

func (a *App) handleAdminListPending(w http.ResponseWriter, r *http.Request, u *User) {
	rows, err := a.db.Query(`SELECT id, username, message, created_at FROM pending_users ORDER BY created_at`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []PendingUser{}
	for rows.Next() {
		var p PendingUser
		if err := rows.Scan(&p.ID, &p.Username, &p.Message, &p.CreatedAt); err != nil {
			serverErr(w, err)
			return
		}
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAdminApprove turns a pending sign-up into a real user with the
// username and password they chose, and removes the request.
func (a *App) handleAdminApprove(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()

	var username, hash string
	err = tx.QueryRow(`SELECT username, password_hash FROM pending_users WHERE id = ?`, id).Scan(&username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "that request no longer exists")
		return
	}
	if err != nil {
		serverErr(w, err)
		return
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
		serverErr(w, err)
		return
	}
	if exists > 0 {
		writeErr(w, http.StatusConflict, "a user named "+username+" already exists; reject this request")
		return
	}
	t := now()
	res, err := tx.Exec(`INSERT INTO users(username, password_hash, is_admin, created_at) VALUES (?, ?, 0, ?)`, username, hash, t)
	if err != nil {
		serverErr(w, err)
		return
	}
	if _, err := tx.Exec(`DELETE FROM pending_users WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	userID, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, AdminUser{ID: userID, Username: username, CreatedAt: t})
}

func (a *App) handleAdminReject(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM pending_users WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleAdminListUsers(w http.ResponseWriter, r *http.Request, u *User) {
	rows, err := a.db.Query(`SELECT id, username, is_admin, created_at, last_login_at FROM users ORDER BY is_admin DESC, username`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []AdminUser{}
	for rows.Next() {
		var x AdminUser
		if err := rows.Scan(&x.ID, &x.Username, &x.IsAdmin, &x.CreatedAt, &x.LastLoginAt); err != nil {
			serverErr(w, err)
			return
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAdminCreateUser mints a regular account directly. If no password is
// given, a random one is generated and returned once.
func (a *App) handleAdminCreateUser(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	generated := ""
	if req.Password == "" {
		generated = randomPassword(16)
		req.Password = generated
	}
	if !validCredentials(w, req.Username, req.Password) {
		return
	}
	if taken, err := usernameTaken(a.db, req.Username); err != nil {
		serverErr(w, err)
		return
	} else if taken {
		writeErr(w, http.StatusConflict, "that username is taken (by a user or a pending request)")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		serverErr(w, err)
		return
	}
	t := now()
	res, err := a.db.Exec(`INSERT INTO users(username, password_hash, is_admin, created_at) VALUES (?, ?, 0, ?)`, req.Username, string(hash), t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "that username is taken")
			return
		}
		serverErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":     AdminUser{ID: id, Username: req.Username, CreatedAt: t},
		"password": generated,
	})
}

// loadManagedUser fetches the target of an admin action and refuses
// yourself and other admins, which are handled through the CLI.
func (a *App) loadManagedUser(w http.ResponseWriter, r *http.Request, u *User) (*AdminUser, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	var x AdminUser
	err := a.db.QueryRow(`SELECT id, username, is_admin, created_at, last_login_at FROM users WHERE id = ?`, id).
		Scan(&x.ID, &x.Username, &x.IsAdmin, &x.CreatedAt, &x.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "user not found")
		return nil, false
	}
	if err != nil {
		serverErr(w, err)
		return nil, false
	}
	if x.ID == u.ID {
		writeErr(w, http.StatusBadRequest, "you can't do that to your own account here")
		return nil, false
	}
	if x.IsAdmin {
		writeErr(w, http.StatusForbidden, "admin accounts are managed from the server command line")
		return nil, false
	}
	return &x, true
}

func (a *App) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request, u *User) {
	x, ok := a.loadManagedUser(w, r, u)
	if !ok {
		return
	}
	// Sessions and votes cascade; notes, boards and games stay with no author.
	if _, err := a.db.Exec(`DELETE FROM users WHERE id = ?`, x.ID); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleAdminResetPassword(w http.ResponseWriter, r *http.Request, u *User) {
	x, ok := a.loadManagedUser(w, r, u)
	if !ok {
		return
	}
	pw := randomPassword(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		serverErr(w, err)
		return
	}
	if _, err := a.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), x.ID); err != nil {
		serverErr(w, err)
		return
	}
	if _, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, x.ID); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"password": pw})
}
