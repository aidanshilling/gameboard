package main

import (
	"net/http"
	"strconv"
)

// Board permissions: everyone can view every board, vote and leave notes.
// Changing a board's games, columns or details takes being its creator, an
// editor the creator added, or an admin with edit mode switched on. Only the
// creator or such an admin can manage editors, answer join requests or delete
// the board.

type Member struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	// Only filled by handleListUsers: picture version (0 = none), and whether
	// it's pixel art to be shown unsmoothed.
	Avatar      int64 `json:"avatar,omitempty"`
	AvatarPixel bool  `json:"avatarPixel,omitempty"`
}

type JoinRequest struct {
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"createdAt"`
}

func canManage(u *User, b *Board) bool {
	return u.boardOverride() || (b.CreatedByID != 0 && b.CreatedByID == u.ID)
}

func (a *App) canEdit(u *User, boardID int64) (bool, error) {
	if u.boardOverride() {
		return true, nil
	}
	var n int
	err := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM boards WHERE id = ? AND created_by = ?) +
		(SELECT COUNT(*) FROM board_members WHERE board_id = ? AND user_id = ?)`,
		boardID, u.ID, boardID, u.ID).Scan(&n)
	return n > 0, err
}

// requireEdit writes a 403 and returns false unless u may change the board.
func (a *App) requireEdit(w http.ResponseWriter, u *User, boardID int64) bool {
	ok, err := a.canEdit(u, boardID)
	if err != nil {
		serverErr(w, err)
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "you can only view this board; ask its owner to add you as an editor")
		return false
	}
	return true
}

// fillAccess adds each board's editors, join requests and what u may do with it.
func (a *App) fillAccess(u *User, boards ...*Board) error {
	byID := map[int64]*Board{}
	for _, b := range boards {
		b.Members = []Member{}
		b.Requests = []JoinRequest{}
		b.CanManage = canManage(u, b)
		b.CanEdit = b.CanManage
		byID[b.ID] = b
	}

	rows, err := a.db.Query(`SELECT bm.board_id, u.id, u.username FROM board_members bm
		JOIN users u ON u.id = bm.user_id ORDER BY bm.created_at, u.username`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var boardID int64
		var m Member
		if err := rows.Scan(&boardID, &m.ID, &m.Username); err != nil {
			rows.Close()
			return err
		}
		if b := byID[boardID]; b != nil {
			b.Members = append(b.Members, m)
			if m.ID == u.ID {
				b.CanEdit = true
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = a.db.Query(`SELECT r.board_id, u.id, u.username, r.created_at FROM board_join_requests r
		JOIN users u ON u.id = r.user_id ORDER BY r.created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var boardID int64
		var jr JoinRequest
		if err := rows.Scan(&boardID, &jr.UserID, &jr.Username, &jr.CreatedAt); err != nil {
			return err
		}
		b := byID[boardID]
		if b == nil {
			continue
		}
		if jr.UserID == u.ID {
			b.Requested = true
		}
		if b.CanManage {
			b.Requests = append(b.Requests, jr)
		}
	}
	return rows.Err()
}

// writeBoard responds with a board and the caller's access to it.
func (a *App) writeBoard(w http.ResponseWriter, status int, u *User, id int64) {
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
	writeJSON(w, status, b)
}

// handleListUsers gives everyone the list of usernames, for picking editors,
// along with which have profile pictures.
func (a *App) handleListUsers(w http.ResponseWriter, r *http.Request, u *User) {
	rows, err := a.db.Query(`SELECT u.id, u.username, COALESCE(av.version, 0), COALESCE(av.pixel, 0) FROM users u
		LEFT JOIN avatars av ON av.user_id = u.id ORDER BY u.username`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Username, &m.Avatar, &m.AvatarPixel); err != nil {
			serverErr(w, err)
			return
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// loadBoardParam fetches the board named by the {id} path value, or writes an error.
func (a *App) loadBoardParam(w http.ResponseWriter, r *http.Request) (*Board, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	b, err := a.getBoard(id)
	if err != nil {
		serverErr(w, err)
		return nil, false
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "board not found")
		return nil, false
	}
	return b, true
}

func userIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

func (a *App) handleAddMember(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	if !canManage(u, b) {
		writeErr(w, http.StatusForbidden, "only the board's owner or an admin can add editors")
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.UserID == b.CreatedByID {
		writeErr(w, http.StatusBadRequest, "the owner can already edit this board")
		return
	}
	var exists int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, req.UserID).Scan(&exists); err != nil {
		serverErr(w, err)
		return
	}
	if exists == 0 {
		writeErr(w, http.StatusBadRequest, "user not found")
		return
	}
	if err := a.addMember(r, b.ID, req.UserID, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeBoard(w, http.StatusOK, u, b.ID)
}

// addMember makes a user an editor and clears any join request they had open.
func (a *App) addMember(r *http.Request, boardID, userID, addedBy int64) error {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO board_members(board_id, user_id, added_by, created_at) VALUES (?, ?, ?, ?)`,
		boardID, userID, addedBy, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM board_join_requests WHERE board_id = ? AND user_id = ?`, boardID, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.touchBoard(boardID)
	return nil
}

// handleRemoveMember takes an editor off a board. Editors may also remove themselves.
func (a *App) handleRemoveMember(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	if !canManage(u, b) && userID != u.ID {
		writeErr(w, http.StatusForbidden, "only the board's owner or an admin can remove editors")
		return
	}
	res, err := a.db.Exec(`DELETE FROM board_members WHERE board_id = ? AND user_id = ?`, b.ID, userID)
	if err != nil {
		serverErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "that person isn't an editor of this board")
		return
	}
	a.touchBoard(b.ID)
	a.writeBoard(w, http.StatusOK, u, b.ID)
}

// handleRequestJoin asks to become an editor of a board.
func (a *App) handleRequestJoin(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	editor, err := a.canEdit(u, b.ID)
	if err != nil {
		serverErr(w, err)
		return
	}
	if editor {
		writeErr(w, http.StatusBadRequest, "you can already edit this board")
		return
	}
	if b.Personal {
		writeErr(w, http.StatusBadRequest, "this is a personal board, so it isn't taking join requests")
		return
	}
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO board_join_requests(board_id, user_id, created_at) VALUES (?, ?, ?)`,
		b.ID, u.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.writeBoard(w, http.StatusCreated, u, b.ID)
}

// handleApproveJoin turns a join request into editor access.
func (a *App) handleApproveJoin(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	if !canManage(u, b) {
		writeErr(w, http.StatusForbidden, "only the board's owner or an admin can approve requests")
		return
	}
	var pending int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM board_join_requests WHERE board_id = ? AND user_id = ?`, b.ID, userID).
		Scan(&pending); err != nil {
		serverErr(w, err)
		return
	}
	if pending == 0 {
		writeErr(w, http.StatusNotFound, "that request no longer exists")
		return
	}
	if err := a.addMember(r, b.ID, userID, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writeBoard(w, http.StatusOK, u, b.ID)
}

// handleDeleteJoin declines a join request, or lets the requester withdraw it.
func (a *App) handleDeleteJoin(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	if !canManage(u, b) && userID != u.ID {
		writeErr(w, http.StatusForbidden, "only the board's owner or an admin can decline requests")
		return
	}
	res, err := a.db.Exec(`DELETE FROM board_join_requests WHERE board_id = ? AND user_id = ?`, b.ID, userID)
	if err != nil {
		serverErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "that request no longer exists")
		return
	}
	a.writeBoard(w, http.StatusOK, u, b.ID)
}
