package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

// Columns are each board's own categories. A board always has at least one.

type Column struct {
	ID       int64   `json:"id"`
	BoardID  int64   `json:"boardId"`
	Name     string  `json:"name"`
	Color    string  `json:"color"`
	Position float64 `json:"position"`
}

var columnColors = []string{"periwinkle", "peach", "ice", "mint", "lilac", "sun", "rose", "stone"}

func validColor(c string) bool {
	for _, x := range columnColors {
		if x == c {
			return true
		}
	}
	return false
}

// starterColumns are what a brand-new board begins with.
var starterColumns = []struct{ name, color string }{
	{"Wishlist", "periwinkle"},
	{"Playing", "mint"},
	{"Finished", "lilac"},
}

const maxColumns = 20

func (a *App) queryColumns(boardID int64) ([]Column, error) {
	rows, err := a.db.Query(`SELECT id, board_id, name, color, position FROM board_columns
		WHERE board_id = ? ORDER BY position, id`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := []Column{}
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.ID, &c.BoardID, &c.Name, &c.Color, &c.Position); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func (a *App) getColumn(id int64) (*Column, error) {
	var c Column
	err := a.db.QueryRow(`SELECT id, board_id, name, color, position FROM board_columns WHERE id = ?`, id).
		Scan(&c.ID, &c.BoardID, &c.Name, &c.Color, &c.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// columnForBoard returns want if it's a column on the board, the board's
// first column if want is nil, or 0 if want belongs elsewhere.
func (a *App) columnForBoard(boardID int64, want *int64) (int64, error) {
	var id int64
	var err error
	if want == nil {
		err = a.db.QueryRow(`SELECT id FROM board_columns WHERE board_id = ? ORDER BY position, id LIMIT 1`, boardID).Scan(&id)
	} else {
		err = a.db.QueryRow(`SELECT id FROM board_columns WHERE id = ? AND board_id = ?`, *want, boardID).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func validColumnName(w http.ResponseWriter, s string) (string, bool) {
	name, ok := cleanText(s, 40)
	if name == "" || !ok {
		writeErr(w, http.StatusBadRequest, "column name must be 1-40 characters")
		return "", false
	}
	return name, true
}

func (a *App) handleAddColumn(w http.ResponseWriter, r *http.Request, u *User) {
	boardID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, ok := validColumnName(w, req.Name)
	if !ok {
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
	var count int
	var maxPos float64
	if err := a.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(position), 0) FROM board_columns WHERE board_id = ?`, boardID).
		Scan(&count, &maxPos); err != nil {
		serverErr(w, err)
		return
	}
	if count >= maxColumns {
		writeErr(w, http.StatusBadRequest, "a board can have at most "+strconv.Itoa(maxColumns)+" columns")
		return
	}
	color := req.Color
	if color == "" {
		color = columnColors[count%len(columnColors)]
	}
	if !validColor(color) {
		writeErr(w, http.StatusBadRequest, "invalid colour")
		return
	}
	res, err := a.db.Exec(`INSERT INTO board_columns(board_id, name, color, position, created_at) VALUES (?, ?, ?, ?, ?)`,
		boardID, name, color, maxPos+1, now())
	if err != nil {
		serverErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	a.touchBoard(boardID)
	writeJSON(w, http.StatusCreated, Column{ID: id, BoardID: boardID, Name: name, Color: color, Position: maxPos + 1})
}

func (a *App) handleUpdateColumn(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c, err := a.getColumn(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "column not found")
		return
	}
	if !a.requireEdit(w, u, c.BoardID) {
		return
	}
	if req.Name != nil {
		if c.Name, ok = validColumnName(w, *req.Name); !ok {
			return
		}
	}
	if req.Color != nil {
		if !validColor(*req.Color) {
			writeErr(w, http.StatusBadRequest, "invalid colour")
			return
		}
		c.Color = *req.Color
	}
	if _, err := a.db.Exec(`UPDATE board_columns SET name = ?, color = ? WHERE id = ?`, c.Name, c.Color, id); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(c.BoardID)
	writeJSON(w, http.StatusOK, c)
}

// handleReorderColumns takes every column id of a board in its new order.
func (a *App) handleReorderColumns(w http.ResponseWriter, r *http.Request, u *User) {
	boardID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !a.requireEdit(w, u, boardID) {
		return
	}
	cols, err := a.queryColumns(boardID)
	if err != nil {
		serverErr(w, err)
		return
	}
	onBoard := map[int64]bool{}
	for _, c := range cols {
		onBoard[c.ID] = true
	}
	seen := map[int64]bool{}
	for _, id := range req.IDs {
		if !onBoard[id] || seen[id] {
			writeErr(w, http.StatusBadRequest, "column list doesn't match the board")
			return
		}
		seen[id] = true
	}
	if len(req.IDs) != len(cols) {
		writeErr(w, http.StatusBadRequest, "column list doesn't match the board")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	for i, id := range req.IDs {
		if _, err := tx.Exec(`UPDATE board_columns SET position = ? WHERE id = ?`, i+1, id); err != nil {
			serverErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(boardID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteColumn removes a column. If it still holds games, ?moveTo= must
// name another column on the same board to move them into.
func (a *App) handleDeleteColumn(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := a.getColumn(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "column not found")
		return
	}
	if !a.requireEdit(w, u, c.BoardID) {
		return
	}
	var columns, games int
	if err := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM board_columns WHERE board_id = ?),
		(SELECT COUNT(*) FROM games WHERE column_id = ?)`, c.BoardID, id).Scan(&columns, &games); err != nil {
		serverErr(w, err)
		return
	}
	if columns <= 1 {
		writeErr(w, http.StatusBadRequest, "a board needs at least one column")
		return
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	if games > 0 {
		target, err := strconv.ParseInt(r.URL.Query().Get("moveTo"), 10, 64)
		if err != nil || target == id {
			writeErr(w, http.StatusConflict, "this column has games; choose a column to move them to")
			return
		}
		var targetBoard int64
		var targetMax float64
		err = tx.QueryRow(`SELECT c.board_id, COALESCE((SELECT MAX(position) FROM games WHERE column_id = c.id), 0)
			FROM board_columns c WHERE c.id = ?`, target).Scan(&targetBoard, &targetMax)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && targetBoard != c.BoardID) {
			writeErr(w, http.StatusBadRequest, "target column isn't on this board")
			return
		}
		if err != nil {
			serverErr(w, err)
			return
		}
		// Append the moved games after the target's existing ones, keeping their order.
		var movedMin float64
		if err := tx.QueryRow(`SELECT MIN(position) FROM games WHERE column_id = ?`, id).Scan(&movedMin); err != nil {
			serverErr(w, err)
			return
		}
		if _, err := tx.Exec(`UPDATE games SET column_id = ?, position = position + ? WHERE column_id = ?`,
			target, targetMax-movedMin+1, id); err != nil {
			serverErr(w, err)
			return
		}
	}
	if _, err := tx.Exec(`DELETE FROM board_columns WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(c.BoardID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
