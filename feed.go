package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Chat: short posts, newest first, each optionally linking a game from a
// board or from Steam/IGDB. There's a site-wide chat everyone can post in,
// and one per board that anyone can read but only its editors can post in.
// Authors and admins can delete posts, as can a board's owner in its chat.

const (
	postMaxLen   = 280
	feedPageSize = 30
)

type PostGame struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	BoardID   int64  `json:"boardId"`
	BoardName string `json:"boardName"`
	Image     string `json:"image,omitempty"`
}

// PostLink is a game from Steam or IGDB, not necessarily on any board.
type PostLink struct {
	Title      string `json:"title"`
	Image      string `json:"image,omitempty"`
	SteamAppID *int64 `json:"steamAppId,omitempty"`
	IGDBID     *int64 `json:"igdbId,omitempty"`
	IGDBURL    string `json:"igdbUrl,omitempty"`
}

type Post struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Username  string    `json:"username"`
	Body      string    `json:"body"`
	CreatedAt int64     `json:"createdAt"`
	Game      *PostGame `json:"game"`  // a game on a board
	Link      *PostLink `json:"link"`  // or one from Steam/IGDB
	Music     *Music    `json:"music"` // a song or playlist
}

// gameImage picks a game's picture out of its stored metadata.
func gameImage(meta string) string {
	var m struct {
		HeaderURL string `json:"headerUrl"`
		CoverURL  string `json:"coverUrl"`
	}
	if json.Unmarshal([]byte(meta), &m) != nil {
		return ""
	}
	if m.HeaderURL != "" {
		return m.HeaderURL
	}
	return m.CoverURL
}

const postSelect = `SELECT p.id, p.user_id, u.username, p.body, p.created_at,
	g.id, COALESCE(g.title, ''), COALESCE(g.board_id, 0), COALESCE(b.name, ''), COALESCE(g.meta, ''),
	p.link_title, p.link_image, p.link_steam, p.link_igdb, p.link_igdb_url,
	m.id, COALESCE(m.provider, ''), COALESCE(m.kind, ''), COALESCE(m.ext_id, ''), COALESCE(m.title, ''),
	COALESCE(m.artist, ''), COALESCE(m.thumb, '')
	FROM posts p JOIN users u ON u.id = p.user_id
	LEFT JOIN games g ON g.id = p.game_id LEFT JOIN boards b ON b.id = g.board_id
	LEFT JOIN music m ON m.id = p.music_id `

func (a *App) queryPosts(where string, args ...any) ([]Post, error) {
	rows, err := a.db.Query(postSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts := []Post{}
	for rows.Next() {
		var p Post
		var gameID, linkSteam, linkIGDB, musicID sql.NullInt64
		var g PostGame
		var l PostLink
		var mu Music
		var meta string
		if err := rows.Scan(&p.ID, &p.UserID, &p.Username, &p.Body, &p.CreatedAt,
			&gameID, &g.Title, &g.BoardID, &g.BoardName, &meta,
			&l.Title, &l.Image, &linkSteam, &linkIGDB, &l.IGDBURL,
			&musicID, &mu.Provider, &mu.Kind, &mu.ExtID, &mu.Title, &mu.Artist, &mu.Thumb); err != nil {
			return nil, err
		}
		if musicID.Valid {
			mu.ID = musicID.Int64
			mu.URL = musicURL(mu.Provider, mu.Kind, mu.ExtID)
			p.Music = &mu
		}
		if gameID.Valid {
			g.ID = gameID.Int64
			g.Image = gameImage(meta)
			p.Game = &g
		}
		if l.Title != "" {
			if linkSteam.Valid {
				l.SteamAppID = &linkSteam.Int64
			}
			if linkIGDB.Valid {
				l.IGDBID = &linkIGDB.Int64
			}
			p.Link = &l
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// writeFeed responds with a page of posts, newest first, from the site-wide
// chat (boardID nil) or one board's chat. ?before=<id> pages back.
func (a *App) writeFeed(w http.ResponseWriter, r *http.Request, boardID *int64) {
	where := `WHERE p.board_id IS NULL`
	args := []any{}
	if boardID != nil {
		where = `WHERE p.board_id = ?`
		args = append(args, *boardID)
	}
	if s := r.URL.Query().Get("before"); s != "" {
		before, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid before")
			return
		}
		where += ` AND p.id < ?`
		args = append(args, before)
	}
	posts, err := a.queryPosts(where+` ORDER BY p.id DESC LIMIT ?`, append(args, feedPageSize)...)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSONCached(w, r, map[string]any{"posts": posts, "more": len(posts) == feedPageSize})
}

func (a *App) handleFeed(w http.ResponseWriter, r *http.Request, u *User) {
	a.writeFeed(w, r, nil)
}

// handleBoardFeed is a board's own chat. Anyone can read it.
func (a *App) handleBoardFeed(w http.ResponseWriter, r *http.Request, u *User) {
	if b, ok := a.loadBoardParam(w, r); ok {
		a.writeFeed(w, r, &b.ID)
	}
}

func (a *App) handleAddPost(w http.ResponseWriter, r *http.Request, u *User) {
	a.addPost(w, r, u, nil)
}

// handleAddBoardPost posts in a board's chat, which only its editors can do.
func (a *App) handleAddBoardPost(w http.ResponseWriter, r *http.Request, u *User) {
	b, ok := a.loadBoardParam(w, r)
	if !ok {
		return
	}
	if ok, err := a.canEdit(u, b.ID); err != nil {
		serverErr(w, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "only this board's editors can post in its chat")
		return
	}
	a.addPost(w, r, u, &b.ID)
}

// addPost creates a post in the site-wide chat (boardID nil) or a board's chat.
func (a *App) addPost(w http.ResponseWriter, r *http.Request, u *User, boardID *int64) {
	var req struct {
		Body       string `json:"body"`
		GameID     *int64 `json:"gameId"`     // a game on a board
		SteamAppID *int64 `json:"steamAppId"` // or one from Steam
		IGDBID     *int64 `json:"igdbId"`     // or IGDB
		MusicID    *int64 `json:"musicId"`    // a song or playlist
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Text is optional when the post shares a game or some music.
	attached := req.GameID != nil || req.SteamAppID != nil || req.IGDBID != nil || req.MusicID != nil
	body, ok := cleanText(req.Body, postMaxLen)
	if !ok || (body == "" && !attached) {
		writeErr(w, http.StatusBadRequest, "posts must be 1-"+strconv.Itoa(postMaxLen)+" characters")
		return
	}
	if req.MusicID != nil {
		if mu, err := a.getMusic(`WHERE m.id = ?`, *req.MusicID); err != nil {
			serverErr(w, err)
			return
		} else if mu == nil {
			writeErr(w, http.StatusBadRequest, "that song or playlist isn't available any more")
			return
		}
	}
	if blocked(body) {
		writeErr(w, http.StatusBadRequest, blockedMessage)
		return
	}
	key := "post:" + strconv.FormatInt(u.ID, 10)
	if !a.posts.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "you're posting a lot; give it a few minutes")
		return
	}
	if !validID(req.SteamAppID) || !validID(req.IGDBID) {
		writeErr(w, http.StatusBadRequest, "invalid game id")
		return
	}
	link := &PostLink{}
	switch {
	case req.GameID != nil:
		g, err := a.getGame(*req.GameID)
		if err != nil {
			serverErr(w, err)
			return
		}
		if g == nil {
			writeErr(w, http.StatusBadRequest, "that game isn't on any board any more")
			return
		}
	case req.SteamAppID != nil || req.IGDBID != nil:
		// Look the game up ourselves rather than trusting a title or picture from the browser.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		l, err := a.meta.lookupLink(ctx, req.SteamAppID, req.IGDBID)
		if err != nil {
			log.Printf("post link lookup: %v", err)
			writeErr(w, http.StatusBadRequest, "couldn't look that game up; try again or pick another")
			return
		}
		link = l
	}
	res, err := a.db.Exec(`INSERT INTO posts(user_id, board_id, body, game_id, link_title, link_image, link_steam, link_igdb, link_igdb_url, music_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, boardID, body, req.GameID, link.Title, link.Image, link.SteamAppID, link.IGDBID, link.IGDBURL, req.MusicID, now())
	if err != nil {
		serverErr(w, err)
		return
	}
	a.posts.fail(key) // counts posts towards the rate limit
	id, _ := res.LastInsertId()
	posts, err := a.queryPosts(`WHERE p.id = ?`, id)
	if err != nil || len(posts) == 0 {
		serverErr(w, errors.Join(err, errors.New("new post not found")))
		return
	}
	writeJSON(w, http.StatusCreated, posts[0])
}

// handleDeletePost removes a post: the author's own, any post for an admin,
// and any post in a board's chat for that board's owner.
func (a *App) handleDeletePost(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var author int64
	var boardID sql.NullInt64
	err := a.db.QueryRow(`SELECT user_id, board_id FROM posts WHERE id = ?`, id).Scan(&author, &boardID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "post not found")
		return
	}
	if err != nil {
		serverErr(w, err)
		return
	}
	allowed := author == u.ID || u.IsAdmin
	if !allowed && boardID.Valid {
		b, err := a.getBoard(boardID.Int64)
		if err != nil {
			serverErr(w, err)
			return
		}
		allowed = b != nil && canManage(u, b)
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "you can only delete your own posts")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM posts WHERE id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleFeedGames finds games on any board by title, for linking in a post.
func (a *App) handleFeedGames(w http.ResponseWriter, r *http.Request, u *User) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	rows, err := a.db.Query(`SELECT g.id, g.title, g.board_id, b.name, g.meta FROM games g
		JOIN boards b ON b.id = g.board_id WHERE g.title LIKE ? ESCAPE '\'
		ORDER BY g.updated_at DESC LIMIT 10`, like)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []PostGame{}
	for rows.Next() {
		var g PostGame
		var meta string
		if err := rows.Scan(&g.ID, &g.Title, &g.BoardID, &g.BoardName, &meta); err != nil {
			serverErr(w, err)
			return
		}
		g.Image = gameImage(meta)
		list = append(list, g)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
