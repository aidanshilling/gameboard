package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Themes are sets of colours for the site's CSS variables. The presets live in
// the frontend; people can also make their own, and share them so everyone
// else can use or copy them. Each user's choice is stored on their user row as
// "preset:<name>" or "custom:<theme id>" ("" means the default).

var themePresets = map[string]bool{
	"penguin": true, "poolsuite": true, "midnight": true, "terminal": true, "vaporwave": true, "win95": true,
}

// themeKeys are the colours a theme may set (the frontend maps them to CSS variables).
var themeKeys = map[string]bool{
	"desk": true, "deskInk": true, "paper": true, "surface": true, "ink": true, "muted": true,
	"accent": true, "accentInk": true, "link": true, "beak": true,
	"periwinkle": true, "peach": true, "ice": true, "mint": true, "sun": true, "lilac": true, "rose": true, "stone": true,
}

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

const maxThemesPerUser = 20

type Theme struct {
	ID        int64             `json:"id"`
	UserID    int64             `json:"userId"`
	Owner     string            `json:"owner"`
	Name      string            `json:"name"`
	Vars      map[string]string `json:"vars"`
	Shared    bool              `json:"shared"`
	UpdatedAt int64             `json:"updatedAt"`
}

// validThemeVars checks a theme's colours, lower-casing them.
func validThemeVars(w http.ResponseWriter, vars map[string]string) (map[string]string, bool) {
	out := map[string]string{}
	for k, v := range vars {
		v = strings.ToLower(strings.TrimSpace(v))
		if !themeKeys[k] || !hexColor.MatchString(v) {
			writeErr(w, http.StatusBadRequest, "theme colours must be #rrggbb values for known settings")
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

func validThemeName(w http.ResponseWriter, s string) (string, bool) {
	name, ok := cleanText(s, 40)
	if name == "" || !ok {
		writeErr(w, http.StatusBadRequest, "theme names must be 1-40 characters")
		return "", false
	}
	if blocked(name) {
		writeErr(w, http.StatusBadRequest, blockedMessage)
		return "", false
	}
	return name, true
}

const themeSelect = `SELECT t.id, t.user_id, u.username, t.name, t.vars, t.shared, t.updated_at
	FROM themes t JOIN users u ON u.id = t.user_id `

func (a *App) queryThemes(where string, args ...any) ([]Theme, error) {
	rows, err := a.db.Query(themeSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Theme{}
	for rows.Next() {
		var t Theme
		var vars string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Owner, &t.Name, &vars, &t.Shared, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(vars), &t.Vars); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// themeFor returns a custom theme u may use (their own, or a shared one), or nil.
func (a *App) themeFor(u *User, id int64) (*Theme, error) {
	list, err := a.queryThemes(`WHERE t.id = ? AND (t.user_id = ? OR t.shared = 1)`, id, u.ID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// writeMe responds with the logged-in user plus their theme, resolving a
// custom theme's colours. A custom theme that's gone (deleted or unshared)
// falls back to the default.
func (a *App) writeMe(w http.ResponseWriter, status int, u *User) {
	var choice string
	if err := a.db.QueryRow(`SELECT theme FROM users WHERE id = ?`, u.ID).Scan(&choice); err != nil {
		serverErr(w, err)
		return
	}
	me := struct {
		*User
		Theme     string            `json:"theme"`
		ThemeVars map[string]string `json:"themeVars,omitempty"`
	}{User: u, Theme: choice}
	if id, ok := strings.CutPrefix(choice, "custom:"); ok {
		n, _ := strconv.ParseInt(id, 10, 64)
		t, err := a.themeFor(u, n)
		if err != nil {
			serverErr(w, err)
			return
		}
		if t == nil {
			me.Theme = ""
		} else {
			me.ThemeVars = t.Vars
		}
	}
	writeJSON(w, status, me)
}

// handleSetTheme saves which theme the user has picked.
func (a *App) handleSetTheme(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Theme string `json:"theme"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	switch {
	case req.Theme == "":
	case strings.HasPrefix(req.Theme, "preset:"):
		if !themePresets[strings.TrimPrefix(req.Theme, "preset:")] {
			writeErr(w, http.StatusBadRequest, "unknown preset")
			return
		}
	case strings.HasPrefix(req.Theme, "custom:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(req.Theme, "custom:"), 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "unknown theme")
			return
		}
		t, err := a.themeFor(u, id)
		if err != nil {
			serverErr(w, err)
			return
		}
		if t == nil {
			writeErr(w, http.StatusBadRequest, "that theme isn't available")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "unknown theme")
		return
	}
	if _, err := a.db.Exec(`UPDATE users SET theme = ? WHERE id = ?`, req.Theme, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeMe(w, http.StatusOK, u)
}

// handleListThemes returns the caller's themes and everyone else's shared ones.
func (a *App) handleListThemes(w http.ResponseWriter, r *http.Request, u *User) {
	mine, err := a.queryThemes(`WHERE t.user_id = ? ORDER BY t.updated_at DESC`, u.ID)
	if err != nil {
		serverErr(w, err)
		return
	}
	shared, err := a.queryThemes(`WHERE t.shared = 1 AND t.user_id != ? ORDER BY t.updated_at DESC`, u.ID)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mine": mine, "shared": shared})
}

func (a *App) handleCreateTheme(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Name   string            `json:"name"`
		Vars   map[string]string `json:"vars"`
		Shared bool              `json:"shared"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, ok := validThemeName(w, req.Name)
	if !ok {
		return
	}
	vars, ok := validThemeVars(w, req.Vars)
	if !ok {
		return
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM themes WHERE user_id = ?`, u.ID).Scan(&count); err != nil {
		serverErr(w, err)
		return
	}
	if count >= maxThemesPerUser {
		writeErr(w, http.StatusBadRequest, "you can have up to "+strconv.Itoa(maxThemesPerUser)+" themes; delete one first")
		return
	}
	data, _ := json.Marshal(vars)
	t := now()
	res, err := a.db.Exec(`INSERT INTO themes(user_id, name, vars, shared, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, name, string(data), req.Shared, t, t)
	if err != nil {
		serverErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	a.writeTheme(w, http.StatusCreated, id)
}

func (a *App) writeTheme(w http.ResponseWriter, status int, id int64) {
	list, err := a.queryThemes(`WHERE t.id = ?`, id)
	if err != nil || len(list) == 0 {
		serverErr(w, errors.Join(err, errors.New("theme not found")))
		return
	}
	writeJSON(w, status, list[0])
}

// loadOwnTheme fetches a theme by its {id} for changing it: owners only, plus
// admins when deleting (allowAdmin).
func (a *App) loadOwnTheme(w http.ResponseWriter, r *http.Request, u *User, allowAdmin bool) (*Theme, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	list, err := a.queryThemes(`WHERE t.id = ?`, id)
	if err != nil {
		serverErr(w, err)
		return nil, false
	}
	if len(list) == 0 {
		writeErr(w, http.StatusNotFound, "theme not found")
		return nil, false
	}
	t := &list[0]
	if t.UserID != u.ID && !(allowAdmin && u.IsAdmin) {
		writeErr(w, http.StatusForbidden, "that's someone else's theme; copy it to make your own version")
		return nil, false
	}
	return t, true
}

func (a *App) handleUpdateTheme(w http.ResponseWriter, r *http.Request, u *User) {
	t, ok := a.loadOwnTheme(w, r, u, false)
	if !ok {
		return
	}
	var req struct {
		Name   *string           `json:"name"`
		Vars   map[string]string `json:"vars"`
		Shared *bool             `json:"shared"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		if t.Name, ok = validThemeName(w, *req.Name); !ok {
			return
		}
	}
	if req.Vars != nil {
		if t.Vars, ok = validThemeVars(w, req.Vars); !ok {
			return
		}
	}
	if req.Shared != nil {
		t.Shared = *req.Shared
	}
	data, _ := json.Marshal(t.Vars)
	if _, err := a.db.Exec(`UPDATE themes SET name = ?, vars = ?, shared = ?, updated_at = ? WHERE id = ?`,
		t.Name, string(data), t.Shared, now(), t.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeTheme(w, http.StatusOK, t.ID)
}

// handleDeleteTheme removes a theme (its owner, or an admin for moderation).
// Anyone using it goes back to the default.
func (a *App) handleDeleteTheme(w http.ResponseWriter, r *http.Request, u *User) {
	t, ok := a.loadOwnTheme(w, r, u, true)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM themes WHERE id = ?`, t.ID); err != nil {
		serverErr(w, err)
		return
	}
	if _, err := tx.Exec(`UPDATE users SET theme = '' WHERE theme = ?`, "custom:"+strconv.FormatInt(t.ID, 10)); err != nil {
		serverErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
