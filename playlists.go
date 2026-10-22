package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Playlists: people's own lists of songs (from Spotify and YouTube, mixed),
// made on the Music page. Only a playlist's owner can change it; anyone can
// play it, queue it, link it in chat or put it on a game (if they edit that
// game's board). Links show the playlist as it is now, not as it was.

const (
	maxPlaylistsPerUser  = 50
	maxPlaylistItems     = 200
	maxPlaylistsPerGame  = 10
	playlistNameMaxLen   = 60
	errPlaylistNotFound  = "playlist not found"
	errPlaylistOwnerOnly = "only the playlist's owner can change it"
)

type Playlist struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	Count     int    `json:"count"`
	Thumb     string `json:"thumb,omitempty"` // the first song's artwork
	UpdatedAt int64  `json:"updatedAt"`
}

// PlaylistDetail is one playlist with its songs.
type PlaylistDetail struct {
	Playlist
	Items []Music `json:"items"`
}

const playlistSelect = `SELECT p.id, p.name, p.user_id, u.username, p.updated_at,
	(SELECT COUNT(*) FROM playlist_items pi WHERE pi.playlist_id = p.id),
	COALESCE((SELECT m.thumb FROM playlist_items pi JOIN music m ON m.id = pi.music_id
		WHERE pi.playlist_id = p.id AND m.thumb != '' ORDER BY pi.position LIMIT 1), '')
	FROM playlists p JOIN users u ON u.id = p.user_id `

func (a *App) queryPlaylists(where string, args ...any) ([]Playlist, error) {
	rows, err := a.db.Query(playlistSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Playlist{}
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.UserID, &p.Username, &p.UpdatedAt, &p.Count, &p.Thumb); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (a *App) getPlaylist(id int64) (*Playlist, error) {
	list, err := a.queryPlaylists(`WHERE p.id = ?`, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// playlistsByID looks up several playlists at once (for chat posts).
func (a *App) playlistsByID(ids []int64) (map[int64]*Playlist, error) {
	out := map[int64]*Playlist{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	list, err := a.queryPlaylists(`WHERE p.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	for i := range list {
		out[list[i].ID] = &list[i]
	}
	return out, nil
}

func (a *App) playlistItems(id int64) ([]Music, error) {
	rows, err := a.db.Query(`SELECT `+musicCols+` FROM playlist_items pi JOIN music m ON m.id = pi.music_id
		WHERE pi.playlist_id = ? ORDER BY pi.position, pi.added_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Music{}
	for rows.Next() {
		mu, err := scanMusic(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *mu)
	}
	return list, rows.Err()
}

// loadPlaylist finds the playlist named in the URL. With owner set, it also
// requires the user to own it.
func (a *App) loadPlaylist(w http.ResponseWriter, r *http.Request, u *User, owner bool) *Playlist {
	id, ok := pathID(w, r)
	if !ok {
		return nil
	}
	p, err := a.getPlaylist(id)
	if err != nil {
		serverErr(w, err)
		return nil
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, errPlaylistNotFound)
		return nil
	}
	if owner && p.UserID != u.ID {
		writeErr(w, http.StatusForbidden, errPlaylistOwnerOnly)
		return nil
	}
	return p
}

// writePlaylist responds with a playlist and its songs.
func (a *App) writePlaylist(w http.ResponseWriter, status int, id int64) {
	p, err := a.getPlaylist(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, errPlaylistNotFound)
		return
	}
	items, err := a.playlistItems(id)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, status, PlaylistDetail{*p, items})
}

func (a *App) touchPlaylist(id int64) error {
	_, err := a.db.Exec(`UPDATE playlists SET updated_at = ? WHERE id = ?`, now(), id)
	return err
}

// playlistName checks a name, writing an error if it won't do.
func playlistName(w http.ResponseWriter, raw string) (string, bool) {
	name, ok := cleanText(raw, playlistNameMaxLen)
	if !ok || name == "" {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("playlist names must be 1-%d characters", playlistNameMaxLen))
		return "", false
	}
	if blocked(name) {
		writeErr(w, http.StatusBadRequest, blockedMessage)
		return "", false
	}
	return name, true
}

// handleListPlaylists lists everyone's playlists, most recently changed first.
func (a *App) handleListPlaylists(w http.ResponseWriter, r *http.Request, u *User) {
	list, err := a.queryPlaylists(`ORDER BY p.updated_at DESC, p.id DESC`)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *App) handleCreatePlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Name    string `json:"name"`
		MusicID *int64 `json:"musicId"` // optional first song
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, ok := playlistName(w, req.Name)
	if !ok {
		return
	}
	if full, err := a.atLimit(maxPlaylistsPerUser, `SELECT COUNT(*) FROM playlists WHERE user_id = ?`, u.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("you can have up to %d playlists; delete one first", maxPlaylistsPerUser))
		return
	}
	if req.MusicID != nil {
		if mu, err := a.getMusic(`WHERE m.id = ?`, *req.MusicID); err != nil {
			serverErr(w, err)
			return
		} else if mu == nil {
			writeErr(w, http.StatusBadRequest, "that song isn't available any more")
			return
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	t := now()
	res, err := tx.Exec(`INSERT INTO playlists(user_id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, u.ID, name, t, t)
	if err != nil {
		serverErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if req.MusicID != nil {
		if _, err := tx.Exec(`INSERT INTO playlist_items(playlist_id, music_id, position, added_at) VALUES (?, ?, 1, ?)`, id, *req.MusicID, t); err != nil {
			serverErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	a.writePlaylist(w, http.StatusCreated, id)
}

func (a *App) handleGetPlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	if p := a.loadPlaylist(w, r, u, false); p != nil {
		a.writePlaylist(w, http.StatusOK, p.ID)
	}
}

func (a *App) handleRenamePlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	p := a.loadPlaylist(w, r, u, true)
	if p == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, ok := playlistName(w, req.Name)
	if !ok {
		return
	}
	if _, err := a.db.Exec(`UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?`, name, now(), p.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writePlaylist(w, http.StatusOK, p.ID)
}

// handleDeletePlaylist deletes a playlist: its owner's, or any for an admin
// in edit mode. Chat posts that linked it stay, without it.
func (a *App) handleDeletePlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	p := a.loadPlaylist(w, r, u, false)
	if p == nil {
		return
	}
	if p.UserID != u.ID && !u.boardOverride() {
		writeErr(w, http.StatusForbidden, errPlaylistOwnerOnly)
		return
	}
	if _, err := a.db.Exec(`DELETE FROM playlists WHERE id = ?`, p.ID); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAddPlaylistItem adds a song to the end of a playlist (once).
func (a *App) handleAddPlaylistItem(w http.ResponseWriter, r *http.Request, u *User) {
	p := a.loadPlaylist(w, r, u, true)
	if p == nil {
		return
	}
	var req struct {
		MusicID int64 `json:"musicId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if mu, err := a.getMusic(`WHERE m.id = ?`, req.MusicID); err != nil {
		serverErr(w, err)
		return
	} else if mu == nil {
		writeErr(w, http.StatusBadRequest, "that song isn't available any more")
		return
	}
	if p.Count >= maxPlaylistItems {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a playlist can hold up to %d songs", maxPlaylistItems))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO playlist_items(playlist_id, music_id, position, added_at)
		VALUES (?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM playlist_items WHERE playlist_id = ?), ?)
		ON CONFLICT(playlist_id, music_id) DO NOTHING`, p.ID, req.MusicID, p.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	if err := a.touchPlaylist(p.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writePlaylist(w, http.StatusOK, p.ID)
}

func (a *App) handleRemovePlaylistItem(w http.ResponseWriter, r *http.Request, u *User) {
	p := a.loadPlaylist(w, r, u, true)
	if p == nil {
		return
	}
	musicID, err := strconv.ParseInt(r.PathValue("musicId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM playlist_items WHERE playlist_id = ? AND music_id = ?`, p.ID, musicID); err != nil {
		serverErr(w, err)
		return
	}
	if err := a.touchPlaylist(p.ID); err != nil {
		serverErr(w, err)
		return
	}
	a.writePlaylist(w, http.StatusOK, p.ID)
}

// handleOrderPlaylist puts a playlist's songs in a new order. The list must
// hold exactly the songs already on it.
func (a *App) handleOrderPlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	p := a.loadPlaylist(w, r, u, true)
	if p == nil {
		return
	}
	var req struct {
		MusicIDs []int64 `json:"musicIds"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	items, err := a.playlistItems(p.ID)
	if err != nil {
		serverErr(w, err)
		return
	}
	have := map[int64]bool{}
	for _, m := range items {
		have[m.ID] = true
	}
	seen := map[int64]bool{}
	for _, id := range req.MusicIDs {
		if !have[id] || seen[id] {
			writeErr(w, http.StatusBadRequest, "the playlist changed; reload and try again")
			return
		}
		seen[id] = true
	}
	if len(seen) != len(have) {
		writeErr(w, http.StatusBadRequest, "the playlist changed; reload and try again")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	for i, id := range req.MusicIDs {
		if _, err := tx.Exec(`UPDATE playlist_items SET position = ? WHERE playlist_id = ? AND music_id = ?`, i+1, p.ID, id); err != nil {
			serverErr(w, err)
			return
		}
	}
	if _, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE id = ?`, now(), p.ID); err != nil {
		serverErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	a.writePlaylist(w, http.StatusOK, p.ID)
}

// gamePlaylists are the playlists on a game, in the order they were added.
func (a *App) gamePlaylists(gameID int64) ([]Playlist, error) {
	return a.queryPlaylists(`JOIN game_playlists gp ON gp.playlist_id = p.id WHERE gp.game_id = ?
		ORDER BY gp.created_at, p.id`, gameID)
}

// handleAddGamePlaylist puts a playlist on a game (board editors only).
func (a *App) handleAddGamePlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	var req struct {
		PlaylistID int64 `json:"playlistId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p, err := a.getPlaylist(req.PlaylistID); err != nil {
		serverErr(w, err)
		return
	} else if p == nil {
		writeErr(w, http.StatusBadRequest, errPlaylistNotFound)
		return
	}
	if full, err := a.atLimit(maxPlaylistsPerGame, `SELECT COUNT(*) FROM game_playlists WHERE game_id = ?`, g.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a game can have up to %d playlists", maxPlaylistsPerGame))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO game_playlists(game_id, playlist_id, added_by, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(game_id, playlist_id) DO NOTHING`, g.ID, req.PlaylistID, u.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

func (a *App) handleRemoveGamePlaylist(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	playlistID, err := strconv.ParseInt(r.PathValue("playlistId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM game_playlists WHERE game_id = ? AND playlist_id = ?`, g.ID, playlistID); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

// checkPlaylistID writes an error if a post's playlist doesn't exist.
func (a *App) checkPlaylistID(w http.ResponseWriter, id int64) bool {
	p, err := a.getPlaylist(id)
	if err != nil {
		serverErr(w, err)
		return false
	}
	if p == nil {
		writeErr(w, http.StatusBadRequest, "that playlist isn't available any more")
		return false
	}
	return true
}
