package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

// writeJSONCached is writeJSON for responses the page polls. It tags the body
// with a hash of its contents (ETag) and, when the page sends back the tag
// of the version it already has, answers 304 Not Modified with no body. The
// hash covers everything in the response, so no change can be missed.
func writeJSONCached(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		serverErr(w, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverErr(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeErr(w, http.StatusInternalServerError, "internal server error")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

// cleanText trims s and reports whether it fits in max characters.
func cleanText(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) <= max
}

// ---- limits ----

// Caps that keep boards quick to load and cheap to poll. Disk isn't the
// constraint (a game with its metadata is ~1.5 KB); bandwidth and how usable
// a huge board is are.
const (
	maxBoardsPerUser = 30  // boards someone owns
	maxGamesPerBoard = 300 // keeps a board's poll under ~60 KB compressed
	maxNotesPerGame  = 200
)

// roomOnBoard writes an error and returns false if a board already holds
// maxGamesPerBoard games.
func (a *App) roomOnBoard(w http.ResponseWriter, boardID int64) bool {
	full, err := a.atLimit(maxGamesPerBoard, `SELECT COUNT(*) FROM games WHERE board_id = ?`, boardID)
	if err != nil {
		serverErr(w, err)
		return false
	}
	if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a board can hold up to %d games; remove some or use another board", maxGamesPerBoard))
		return false
	}
	return true
}

// atLimit counts rows with a query and reports whether there are already max.
func (a *App) atLimit(max int, query string, args ...any) (bool, error) {
	var n int
	err := a.db.QueryRow(query, args...).Scan(&n)
	return n >= max, err
}

// ---- boards ----

type Board struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Personal    bool   `json:"personal"` // nobody can request to join
	CreatedByID int64  `json:"createdById"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	GameCount   int    `json:"gameCount"`

	// Filled in per viewer by fillAccess.
	Members   []Member      `json:"members"` // editors besides the creator
	CanEdit   bool          `json:"canEdit"`
	CanManage bool          `json:"canManage"`
	Requested bool          `json:"requested"` // the viewer has asked to join
	Requests  []JoinRequest `json:"requests"`  // pending requests; only shown to those who can manage
}

const boardSelect = `SELECT b.id, b.name, b.description, b.personal, COALESCE(b.created_by, 0), COALESCE(u.username, ''),
	b.created_at, b.updated_at, (SELECT COUNT(*) FROM games g WHERE g.board_id = b.id)
	FROM boards b LEFT JOIN users u ON u.id = b.created_by `

func scanBoard(s interface{ Scan(...any) error }) (*Board, error) {
	var b Board
	err := s.Scan(&b.ID, &b.Name, &b.Description, &b.Personal, &b.CreatedByID, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt, &b.GameCount)
	return &b, err
}

func (a *App) getBoard(id int64) (*Board, error) {
	b, err := scanBoard(a.db.QueryRow(boardSelect+`WHERE b.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func (a *App) touchBoard(id int64) {
	if _, err := a.db.Exec(`UPDATE boards SET updated_at = ? WHERE id = ?`, now(), id); err != nil {
		log.Printf("touch board %d: %v", id, err)
	}
}

func (a *App) handleListBoards(w http.ResponseWriter, r *http.Request, u *User) {
	rows, err := a.db.Query(boardSelect + `ORDER BY b.updated_at DESC`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	boards := []*Board{}
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			serverErr(w, err)
			return
		}
		boards = append(boards, b)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	if err := a.fillAccess(u, boards...); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boards)
}

func (a *App) handleCreateBoard(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Personal    bool   `json:"personal"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, ok := cleanText(req.Name, 100)
	if name == "" || !ok {
		writeErr(w, http.StatusBadRequest, "board name must be 1-100 characters")
		return
	}
	desc, ok := cleanText(req.Description, 1000)
	if !ok {
		writeErr(w, http.StatusBadRequest, "description is too long")
		return
	}
	if full, err := a.atLimit(maxBoardsPerUser, `SELECT COUNT(*) FROM boards WHERE created_by = ?`, u.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("you can own up to %d boards; delete one first", maxBoardsPerUser))
		return
	}
	t := now()
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO boards(name, description, personal, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, desc, req.Personal, u.ID, t, t)
	if err != nil {
		serverErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	for i, c := range starterColumns {
		if _, err := tx.Exec(`INSERT INTO board_columns(board_id, name, color, position, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, c.name, c.color, i+1, t); err != nil {
			serverErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	a.writeBoard(w, http.StatusCreated, u, id)
}

func (a *App) handleGetBoard(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := a.getBoard(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "board not found")
		return
	}
	if err := a.fillAccess(u, b); err != nil {
		serverErr(w, err)
		return
	}
	columns, err := a.queryColumns(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	games, err := a.queryGames(`WHERE g.board_id = ?`, id)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSONCached(w, r, map[string]any{"board": b, "columns": columns, "games": games})
}

func (a *App) handleUpdateBoard(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Personal    *bool   `json:"personal"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	b, err := a.getBoard(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "board not found")
		return
	}
	if !a.requireEdit(w, u, id) {
		return
	}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 100)
		if name == "" || !ok {
			writeErr(w, http.StatusBadRequest, "board name must be 1-100 characters")
			return
		}
		b.Name = name
	}
	if req.Description != nil {
		desc, ok := cleanText(*req.Description, 1000)
		if !ok {
			writeErr(w, http.StatusBadRequest, "description is too long")
			return
		}
		b.Description = desc
	}
	if req.Personal != nil {
		if !canManage(u, b) {
			writeErr(w, http.StatusForbidden, "only the board's owner or an admin can make it personal")
			return
		}
		b.Personal = *req.Personal
	}
	if _, err := a.db.Exec(`UPDATE boards SET name = ?, description = ?, personal = ?, updated_at = ? WHERE id = ?`,
		b.Name, b.Description, b.Personal, now(), id); err != nil {
		serverErr(w, err)
		return
	}
	if b.Personal {
		// A personal board takes no join requests, including ones already waiting.
		if _, err := a.db.Exec(`DELETE FROM board_join_requests WHERE board_id = ?`, id); err != nil {
			serverErr(w, err)
			return
		}
	}
	a.writeBoard(w, http.StatusOK, u, id)
}

func (a *App) handleDeleteBoard(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := a.getBoard(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "board not found")
		return
	}
	if !canManage(u, b) {
		writeErr(w, http.StatusForbidden, "only the board's owner or an admin can delete it")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM boards WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- games ----

type Game struct {
	ID            int64           `json:"id"`
	BoardID       int64           `json:"boardId"`
	Title         string          `json:"title"`
	ColumnID      int64           `json:"columnId"`
	Position      float64         `json:"position"`
	SteamAppID    *int64          `json:"steamAppId"`
	IGDBID        *int64          `json:"igdbId"`
	Meta          json.RawMessage `json:"meta"`
	MetaUpdatedAt int64           `json:"metaUpdatedAt"`
	AddedBy       string          `json:"addedBy"`
	CreatedAt     int64           `json:"createdAt"`
	UpdatedAt     int64           `json:"updatedAt"`
	NoteCount     int             `json:"noteCount"`
	MusicCount    int             `json:"musicCount"`
	Interests     []Interest      `json:"interests"`
}

type Interest struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Level    int    `json:"level"`
	Owns     bool   `json:"owns"`
}

const gameSelect = `SELECT g.id, g.board_id, g.title, COALESCE(g.column_id, 0), g.position, g.steam_appid, g.igdb_id,
	g.meta, g.meta_updated_at, COALESCE(u.username, ''), g.created_at, g.updated_at,
	(SELECT COUNT(*) FROM notes n WHERE n.game_id = g.id),
	(SELECT COUNT(*) FROM game_music gm WHERE gm.game_id = g.id)
	FROM games g LEFT JOIN users u ON u.id = g.added_by `

// queryGames loads games matching where (which may reference alias g) along with their interests.
func (a *App) queryGames(where string, args ...any) ([]*Game, error) {
	rows, err := a.db.Query(gameSelect+where+` ORDER BY g.position`, args...)
	if err != nil {
		return nil, err
	}
	games := []*Game{}
	byID := map[int64]*Game{}
	for rows.Next() {
		g := &Game{Interests: []Interest{}}
		var meta string
		var steam, igdb sql.NullInt64
		if err := rows.Scan(&g.ID, &g.BoardID, &g.Title, &g.ColumnID, &g.Position, &steam, &igdb,
			&meta, &g.MetaUpdatedAt, &g.AddedBy, &g.CreatedAt, &g.UpdatedAt, &g.NoteCount, &g.MusicCount); err != nil {
			rows.Close()
			return nil, err
		}
		if steam.Valid {
			g.SteamAppID = &steam.Int64
		}
		if igdb.Valid {
			g.IGDBID = &igdb.Int64
		}
		if meta == "" {
			meta = "{}"
		}
		g.Meta = json.RawMessage(meta)
		games = append(games, g)
		byID[g.ID] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return games, nil
	}

	irows, err := a.db.Query(`SELECT i.game_id, i.user_id, u.username, i.level, i.owns
		FROM interests i JOIN users u ON u.id = i.user_id JOIN games g ON g.id = i.game_id `+where+
		` ORDER BY i.level DESC, u.username`, args...)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	for irows.Next() {
		var gameID int64
		var in Interest
		if err := irows.Scan(&gameID, &in.UserID, &in.Username, &in.Level, &in.Owns); err != nil {
			return nil, err
		}
		if g := byID[gameID]; g != nil {
			g.Interests = append(g.Interests, in)
		}
	}
	return games, irows.Err()
}

func (a *App) getGame(id int64) (*Game, error) {
	games, err := a.queryGames(`WHERE g.id = ?`, id)
	if err != nil || len(games) == 0 {
		return nil, err
	}
	return games[0], nil
}

// loadGame fetches a game or writes a 404/500 and returns nil.
func (a *App) loadGame(w http.ResponseWriter, r *http.Request) *Game {
	id, ok := pathID(w, r)
	if !ok {
		return nil
	}
	g, err := a.getGame(id)
	if err != nil {
		serverErr(w, err)
		return nil
	}
	if g == nil {
		writeErr(w, http.StatusNotFound, "game not found")
		return nil
	}
	return g
}

type Note struct {
	ID        int64  `json:"id"`
	GameID    int64  `json:"gameId"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (a *App) queryNotes(gameID int64) ([]Note, error) {
	rows, err := a.db.Query(`SELECT n.id, n.game_id, COALESCE(n.user_id, 0), COALESCE(u.username, ''), n.body, n.created_at, n.updated_at
		FROM notes n LEFT JOIN users u ON u.id = n.user_id WHERE n.game_id = ? ORDER BY n.created_at DESC, n.id DESC`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.GameID, &n.UserID, &n.Username, &n.Body, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// writeGameDetail responds with a game plus its notes; most game mutations return this.
func (a *App) writeGameDetail(w http.ResponseWriter, status int, id int64) {
	g, err := a.getGame(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if g == nil {
		writeErr(w, http.StatusNotFound, "game not found")
		return
	}
	notes, err := a.queryNotes(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	music, err := a.gameMusic(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"game": g, "notes": notes, "music": music})
}

func (a *App) nextPosition(columnID int64) (float64, error) {
	var pos float64
	err := a.db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM games WHERE column_id = ?`, columnID).Scan(&pos)
	return pos, err
}

func (a *App) findDuplicate(boardID int64, steam, igdb *int64) (string, error) {
	if steam == nil && igdb == nil {
		return "", nil
	}
	var title string
	err := a.db.QueryRow(`SELECT title FROM games WHERE board_id = ? AND
		((? IS NOT NULL AND steam_appid = ?) OR (? IS NOT NULL AND igdb_id = ?)) LIMIT 1`,
		boardID, steam, steam, igdb, igdb).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return title, err
}

func (a *App) saveMeta(gameID int64, res FetchResult) error {
	meta, err := json.Marshal(res.Meta)
	if err != nil {
		return err
	}
	t := now()
	_, err = a.db.Exec(`UPDATE games SET steam_appid = ?, igdb_id = ?, meta = ?, meta_updated_at = ?, updated_at = ? WHERE id = ?`,
		res.SteamAppID, res.IGDBID, string(meta), t, t, gameID)
	return err
}

func validID(id *int64) bool { return id == nil || *id > 0 }

func (a *App) handleAddGame(w http.ResponseWriter, r *http.Request, u *User) {
	boardID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title      string `json:"title"`
		SteamAppID *int64 `json:"steamAppId"`
		IGDBID     *int64 `json:"igdbId"`
		ColumnID   *int64 `json:"columnId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	b, err := a.getBoard(boardID)
	if err != nil {
		serverErr(w, err)
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "board not found")
		return
	}
	if !a.requireEdit(w, u, boardID) {
		return
	}
	if !a.roomOnBoard(w, boardID) {
		return
	}
	title, ok := cleanText(req.Title, 200)
	if !ok {
		writeErr(w, http.StatusBadRequest, "title is too long")
		return
	}
	if title == "" && req.SteamAppID == nil && req.IGDBID == nil {
		writeErr(w, http.StatusBadRequest, "a title is required")
		return
	}
	if !validID(req.SteamAppID) || !validID(req.IGDBID) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	columnID, err := a.columnForBoard(boardID, req.ColumnID)
	if err != nil {
		serverErr(w, err)
		return
	}
	if columnID == 0 {
		writeErr(w, http.StatusBadRequest, "that column isn't on this board")
		return
	}
	if dup, err := a.findDuplicate(boardID, req.SteamAppID, req.IGDBID); err != nil {
		serverErr(w, err)
		return
	} else if dup != "" {
		writeErr(w, http.StatusConflict, dup+" is already on this board")
		return
	}

	// Fetch metadata up front so the card shows up complete. Failures only mean less metadata.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := a.meta.Fetch(ctx, req.SteamAppID, req.IGDBID)
	if title == "" {
		title = res.Name
	}
	if title == "" {
		title = "Untitled game"
	}
	// The lookup may have found an id we didn't have yet; check again.
	if dup, err := a.findDuplicate(boardID, res.SteamAppID, res.IGDBID); err != nil {
		serverErr(w, err)
		return
	} else if dup != "" {
		writeErr(w, http.StatusConflict, dup+" is already on this board")
		return
	}

	pos, err := a.nextPosition(columnID)
	if err != nil {
		serverErr(w, err)
		return
	}
	meta, _ := json.Marshal(res.Meta)
	t := now()
	result, err := a.db.Exec(`INSERT INTO games(board_id, title, column_id, position, steam_appid, igdb_id, meta, meta_updated_at, added_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		boardID, title, columnID, pos, res.SteamAppID, res.IGDBID, string(meta), t, u.ID, t, t)
	if err != nil {
		serverErr(w, err)
		return
	}
	id, _ := result.LastInsertId()
	a.touchBoard(boardID)
	g, err := a.getGame(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (a *App) handleGetGame(w http.ResponseWriter, r *http.Request, u *User) {
	if g := a.loadGame(w, r); g != nil {
		a.writeGameDetail(w, http.StatusOK, g.ID)
	}
}

// loadEditableGame is loadGame for changes: it also requires edit rights on the game's board.
func (a *App) loadEditableGame(w http.ResponseWriter, r *http.Request, u *User) *Game {
	g := a.loadGame(w, r)
	if g == nil || !a.requireEdit(w, u, g.BoardID) {
		return nil
	}
	return g
}

func (a *App) handleUpdateGame(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	var req struct {
		Title    *string  `json:"title"`
		ColumnID *int64   `json:"columnId"`
		Position *float64 `json:"position"`
		BoardID  *int64   `json:"boardId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	oldBoard, oldColumn := g.BoardID, g.ColumnID
	if req.Title != nil {
		t, ok := cleanText(*req.Title, 200)
		if t == "" || !ok {
			writeErr(w, http.StatusBadRequest, "title must be 1-200 characters")
			return
		}
		g.Title = t
	}
	if req.BoardID != nil && *req.BoardID != g.BoardID {
		b, err := a.getBoard(*req.BoardID)
		if err != nil {
			serverErr(w, err)
			return
		}
		if b == nil {
			writeErr(w, http.StatusBadRequest, "board not found")
			return
		}
		if !a.requireEdit(w, u, b.ID) || !a.roomOnBoard(w, b.ID) {
			return
		}
		if dup, err := a.findDuplicate(b.ID, g.SteamAppID, g.IGDBID); err != nil {
			serverErr(w, err)
			return
		} else if dup != "" {
			writeErr(w, http.StatusConflict, dup+" is already on that board")
			return
		}
		g.BoardID = b.ID
	}
	// A board move without a column given lands in the new board's first column.
	if req.ColumnID != nil || g.BoardID != oldBoard {
		col, err := a.columnForBoard(g.BoardID, req.ColumnID)
		if err != nil {
			serverErr(w, err)
			return
		}
		if col == 0 {
			writeErr(w, http.StatusBadRequest, "that column isn't on this board")
			return
		}
		g.ColumnID = col
	}
	switch {
	case req.Position != nil:
		if math.IsNaN(*req.Position) || math.IsInf(*req.Position, 0) {
			writeErr(w, http.StatusBadRequest, "invalid position")
			return
		}
		g.Position = *req.Position
	case g.ColumnID != oldColumn:
		// Moved without an explicit spot: put it at the bottom of its new column.
		pos, err := a.nextPosition(g.ColumnID)
		if err != nil {
			serverErr(w, err)
			return
		}
		g.Position = pos
	}
	if _, err := a.db.Exec(`UPDATE games SET title = ?, column_id = ?, position = ?, board_id = ?, updated_at = ? WHERE id = ?`,
		g.Title, g.ColumnID, g.Position, g.BoardID, now(), g.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	if oldBoard != g.BoardID {
		a.touchBoard(oldBoard)
	}
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

func (a *App) handleDeleteGame(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM games WHERE id = ?`, g.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleSetLinks(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	var req struct {
		SteamAppID *int64 `json:"steamAppId"`
		IGDBID     *int64 `json:"igdbId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validID(req.SteamAppID) || !validID(req.IGDBID) {
		writeErr(w, http.StatusBadRequest, "ids must be positive numbers")
		return
	}
	a.refetch(w, r, g, req.SteamAppID, req.IGDBID)
}

func (a *App) handleRefreshGame(w http.ResponseWriter, r *http.Request, u *User) {
	if g := a.loadEditableGame(w, r, u); g != nil {
		a.refetch(w, r, g, g.SteamAppID, g.IGDBID)
	}
}

func (a *App) refetch(w http.ResponseWriter, r *http.Request, g *Game, steam, igdb *int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := a.meta.Fetch(ctx, steam, igdb)
	if err := a.saveMeta(g.ID, res); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

func (a *App) handleSetInterest(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadGame(w, r)
	if g == nil {
		return
	}
	var req struct {
		Level int  `json:"level"`
		Owns  bool `json:"owns"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Level < -1 || req.Level > 2 {
		writeErr(w, http.StatusBadRequest, "invalid interest level")
		return
	}
	var err error
	if req.Level == 0 && !req.Owns {
		_, err = a.db.Exec(`DELETE FROM interests WHERE game_id = ? AND user_id = ?`, g.ID, u.ID)
	} else {
		_, err = a.db.Exec(`INSERT INTO interests(game_id, user_id, level, owns) VALUES (?, ?, ?, ?)
			ON CONFLICT(game_id, user_id) DO UPDATE SET level = excluded.level, owns = excluded.owns`,
			g.ID, u.ID, req.Level, req.Owns)
	}
	if err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

// ---- notes ----

func (a *App) handleAddNote(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadGame(w, r)
	if g == nil {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	body, ok := cleanText(req.Body, 10000)
	if body == "" || !ok {
		writeErr(w, http.StatusBadRequest, "note must be 1-10000 characters")
		return
	}
	if full, err := a.atLimit(maxNotesPerGame, `SELECT COUNT(*) FROM notes WHERE game_id = ?`, g.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a game can have up to %d notes; delete some old ones first", maxNotesPerGame))
		return
	}
	t := now()
	if _, err := a.db.Exec(`INSERT INTO notes(game_id, user_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		g.ID, u.ID, body, t, t); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusCreated, g.ID)
}

// loadNote returns the note's game id and author, or writes an error.
func (a *App) loadNote(w http.ResponseWriter, r *http.Request) (noteID, gameID, authorID int64, ok bool) {
	noteID, ok = pathID(w, r)
	if !ok {
		return
	}
	err := a.db.QueryRow(`SELECT game_id, COALESCE(user_id, 0) FROM notes WHERE id = ?`, noteID).Scan(&gameID, &authorID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "note not found")
		return 0, 0, 0, false
	}
	if err != nil {
		serverErr(w, err)
		return 0, 0, 0, false
	}
	return noteID, gameID, authorID, true
}

func (a *App) handleUpdateNote(w http.ResponseWriter, r *http.Request, u *User) {
	noteID, gameID, authorID, ok := a.loadNote(w, r)
	if !ok {
		return
	}
	if authorID != u.ID {
		writeErr(w, http.StatusForbidden, "you can only edit your own notes")
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	body, ok := cleanText(req.Body, 10000)
	if body == "" || !ok {
		writeErr(w, http.StatusBadRequest, "note must be 1-10000 characters")
		return
	}
	if _, err := a.db.Exec(`UPDATE notes SET body = ?, updated_at = ? WHERE id = ?`, body, now(), noteID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeGameDetail(w, http.StatusOK, gameID)
}

func (a *App) handleDeleteNote(w http.ResponseWriter, r *http.Request, u *User) {
	noteID, gameID, authorID, ok := a.loadNote(w, r)
	if !ok {
		return
	}
	if authorID != u.ID && !u.boardOverride() {
		writeErr(w, http.StatusForbidden, "you can only delete your own notes")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM notes WHERE id = ?`, noteID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeGameDetail(w, http.StatusOK, gameID)
}

// ---- search ----

var (
	steamLinkRe = regexp.MustCompile(`(?:store\.steampowered\.com|steamdb\.info|protondb\.com|steamcommunity\.com)/app/(\d+)`)
	igdbLinkRe  = regexp.MustCompile(`igdb\.com/games/([a-z0-9-]+)`)
)

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request, u *User) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := map[string]any{
		"steam":       []SearchResult{},
		"igdb":        []SearchResult{},
		"igdbEnabled": a.meta.igdbEnabled(),
	}
	if utf8.RuneCountInString(q) < 2 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(q) > 200 {
		q = q[:200]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Pasted links resolve straight to one result.
	if m := steamLinkRe.FindStringSubmatch(q); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if res, err := a.meta.steamResult(ctx, id); err == nil {
			out["steam"] = []SearchResult{res}
		} else {
			log.Printf("steam lookup %d: %v", id, err)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if m := igdbLinkRe.FindStringSubmatch(q); m != nil && a.meta.igdbEnabled() {
		if res, err := a.meta.igdbSearchSlug(ctx, m[1]); err == nil {
			out["igdb"] = res
		} else {
			log.Printf("igdb slug %s: %v", m[1], err)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var wg sync.WaitGroup
	var steamRes, igdbRes []SearchResult
	var steamErr, igdbErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		steamRes, steamErr = a.meta.steamSearch(ctx, q)
	}()
	if a.meta.igdbEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			igdbRes, igdbErr = a.meta.igdbSearch(ctx, q)
		}()
	}
	wg.Wait()

	var errs []string
	if steamErr != nil {
		log.Printf("steam search: %v", steamErr)
		errs = append(errs, "Steam search failed")
	} else {
		out["steam"] = steamRes
	}
	if igdbErr != nil {
		log.Printf("igdb search: %v", igdbErr)
		errs = append(errs, "IGDB search failed")
	} else if igdbRes != nil {
		out["igdb"] = igdbRes
	}
	out["errors"] = errs
	writeJSON(w, http.StatusOK, out)
}
