package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

// The ticker is the scrolling line in the footer. Admins set its default
// messages; anyone can add a few of their own, which last until the weekly
// reset (Monday 00:00 UTC), after which only the defaults remain.

const (
	tickerMaxLen  = 80
	tickerPerUser = 3
)

// initialTicker seeds the defaults the first time the database is migrated.
var initialTicker = []string{
	"when will we finish valheim?",
	"chuck you nanny in a wheelie bin",
	"MYYY WHAAAAAAAATTTT?????",
	"Type system",
	"Chris is gonna fail me...",
	"Edinbrahhh",
	"MAAAAARRRRRRK!!!",
}

type TickerDefault struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

type TickerMessage struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"createdAt"`
}

// weekStart is the most recent Monday 00:00 UTC, when people's messages reset.
func weekStart(t time.Time) time.Time {
	t = t.UTC()
	daysSinceMonday := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-daysSinceMonday, 0, 0, 0, 0, time.UTC)
}

func (a *App) tickerDefaults() ([]TickerDefault, error) {
	rows, err := a.db.Query(`SELECT id, text FROM ticker_defaults ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []TickerDefault{}
	for rows.Next() {
		var d TickerDefault
		if err := rows.Scan(&d.ID, &d.Text); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// tickerMessages returns people's messages from the current week, oldest first.
func (a *App) tickerMessages() ([]TickerMessage, error) {
	rows, err := a.db.Query(`SELECT t.id, t.text, t.user_id, u.username, t.created_at
		FROM ticker_messages t JOIN users u ON u.id = t.user_id
		WHERE t.created_at >= ? ORDER BY t.created_at, t.id`, weekStart(time.Now()).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []TickerMessage{}
	for rows.Next() {
		var m TickerMessage
		if err := rows.Scan(&m.ID, &m.Text, &m.UserID, &m.Username, &m.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// sweepTicker deletes messages from before this week's reset.
func (a *App) sweepTicker() {
	if _, err := a.db.Exec(`DELETE FROM ticker_messages WHERE created_at < ?`, weekStart(time.Now()).Unix()); err != nil {
		log.Printf("sweep ticker: %v", err)
	}
}

func validTickerText(w http.ResponseWriter, s string) (string, bool) {
	text, ok := cleanText(s, tickerMaxLen)
	if text == "" || !ok {
		writeErr(w, http.StatusBadRequest, "ticker messages must be 1-"+strconv.Itoa(tickerMaxLen)+" characters")
		return "", false
	}
	return text, true
}

// writeTicker responds with everything the footer and the ticker dialog need.
func (a *App) writeTicker(w http.ResponseWriter, status int, u *User) {
	defaults, err := a.tickerDefaults()
	if err != nil {
		serverErr(w, err)
		return
	}
	messages, err := a.tickerMessages()
	if err != nil {
		serverErr(w, err)
		return
	}
	items := []string{}
	for _, d := range defaults {
		items = append(items, d.Text)
	}
	mine := []TickerMessage{}
	for _, m := range messages {
		items = append(items, m.Text)
		if m.UserID == u.ID {
			mine = append(mine, m)
		}
	}
	writeJSON(w, status, map[string]any{
		"items":     items,
		"mine":      mine,
		"resetsAt":  weekStart(time.Now()).AddDate(0, 0, 7).Unix(),
		"maxLength": tickerMaxLen,
		"perUser":   tickerPerUser,
	})
}

func (a *App) handleGetTicker(w http.ResponseWriter, r *http.Request, u *User) {
	a.writeTicker(w, http.StatusOK, u)
}

func (a *App) handleAddTicker(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	text, ok := validTickerText(w, req.Text)
	if !ok {
		return
	}
	if blocked(text) {
		writeErr(w, http.StatusBadRequest, blockedMessage)
		return
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM ticker_messages WHERE user_id = ? AND created_at >= ?`,
		u.ID, weekStart(time.Now()).Unix()).Scan(&count); err != nil {
		serverErr(w, err)
		return
	}
	if count >= tickerPerUser {
		writeErr(w, http.StatusBadRequest, "you can have "+strconv.Itoa(tickerPerUser)+" messages on the ticker at once; delete one first")
		return
	}
	if _, err := a.db.Exec(`INSERT INTO ticker_messages(user_id, text, created_at) VALUES (?, ?, ?)`, u.ID, text, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.writeTicker(w, http.StatusCreated, u)
}

// handleDeleteTicker removes one of someone's messages: their own, or anyone's for admins.
func (a *App) handleDeleteTicker(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var owner int64
	err := a.db.QueryRow(`SELECT user_id FROM ticker_messages WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		serverErr(w, err)
		return
	}
	if owner != u.ID && !u.IsAdmin {
		writeErr(w, http.StatusForbidden, "you can only remove your own messages")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM ticker_messages WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	a.writeTicker(w, http.StatusOK, u)
}

// ---- admin ----

func (a *App) writeAdminTicker(w http.ResponseWriter, status int) {
	defaults, err := a.tickerDefaults()
	if err != nil {
		serverErr(w, err)
		return
	}
	messages, err := a.tickerMessages()
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, status, map[string]any{
		"defaults": defaults,
		"messages": messages,
		"resetsAt": weekStart(time.Now()).AddDate(0, 0, 7).Unix(),
	})
}

func (a *App) handleAdminTicker(w http.ResponseWriter, r *http.Request, u *User) {
	a.writeAdminTicker(w, http.StatusOK)
}

func (a *App) handleAdminAddDefault(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	text, ok := validTickerText(w, req.Text)
	if !ok {
		return
	}
	if _, err := a.db.Exec(`INSERT INTO ticker_defaults(text, position, created_at)
		VALUES (?, (SELECT COALESCE(MAX(position), 0) + 1 FROM ticker_defaults), ?)`, text, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.writeAdminTicker(w, http.StatusCreated)
}

func (a *App) handleAdminUpdateDefault(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	text, ok := validTickerText(w, req.Text)
	if !ok {
		return
	}
	res, err := a.db.Exec(`UPDATE ticker_defaults SET text = ? WHERE id = ?`, text, id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	a.writeAdminTicker(w, http.StatusOK)
}

func (a *App) handleAdminDeleteDefault(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM ticker_defaults WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	a.writeAdminTicker(w, http.StatusOK)
}

// handleAdminClearTicker resets the ticker to its defaults now, without waiting for Monday.
func (a *App) handleAdminClearTicker(w http.ResponseWriter, r *http.Request, u *User) {
	if _, err := a.db.Exec(`DELETE FROM ticker_messages`); err != nil {
		serverErr(w, err)
		return
	}
	a.writeAdminTicker(w, http.StatusOK)
}
