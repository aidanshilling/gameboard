package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Music: Spotify and YouTube (Music) links people share in chat, attach to
// games as soundtracks, and play in the floating player. The server only
// accepts links it can parse into a provider, kind and id; it then looks up
// the title and artwork itself (oEmbed). Playback happens in each service's
// own embedded player in the browser; no audio passes through here. Links to
// the same music on other services are just searches the browser builds.

const maxMusicPerGame = 20

type Music struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"` // spotify | youtube
	Kind     string `json:"kind"`     // spotify: track album playlist episode show artist; youtube: video playlist
	ExtID    string `json:"extId"`
	Title    string `json:"title"`
	Artist   string `json:"artist,omitempty"`
	Thumb    string `json:"thumb,omitempty"`
	URL      string `json:"url"`
}

var (
	spotifyURLRe  = regexp.MustCompile(`^https?://open\.spotify\.com/(?:intl-[a-z-]+/)?(track|album|playlist|episode|show|artist)/([A-Za-z0-9]{22})(?:[/?#]|$)`)
	spotifyURIRe  = regexp.MustCompile(`^spotify:(track|album|playlist|episode|show|artist):([A-Za-z0-9]{22})$`)
	youtubeIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	youtubeListRe = regexp.MustCompile(`^[A-Za-z0-9_-]{10,64}$`)

	errMusicLink   = errors.New("that isn't a Spotify or YouTube (Music) link; paste a song, album or playlist link")
	errMusicShort  = errors.New("short spotify.link links can't be looked up; open it and copy the open.spotify.com link instead")
	errMusicBrowse = errors.New("YouTube Music album pages can't be played here; open the album, then Share → Copy link to get its playlist link")
)

// parseMusicURL turns a pasted link into provider, kind and id, or explains
// why it can't.
func parseMusicURL(raw string) (provider, kind, id string, err error) {
	raw = strings.TrimSpace(raw)
	if m := spotifyURIRe.FindStringSubmatch(raw); m != nil {
		return "spotify", m[1], m[2], nil
	}
	if m := spotifyURLRe.FindStringSubmatch(raw); m != nil {
		return "spotify", m[1], m[2], nil
	}
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", "", errMusicLink
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
	q := u.Query()
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com":
		switch {
		case u.Path == "/watch" && youtubeIDRe.MatchString(q.Get("v")):
			return "youtube", "video", q.Get("v"), nil
		case u.Path == "/playlist" && youtubeListRe.MatchString(q.Get("list")):
			return "youtube", "playlist", q.Get("list"), nil
		case strings.HasPrefix(u.Path, "/shorts/") && youtubeIDRe.MatchString(strings.TrimPrefix(u.Path, "/shorts/")):
			return "youtube", "video", strings.TrimPrefix(u.Path, "/shorts/"), nil
		case strings.HasPrefix(u.Path, "/embed/") && youtubeIDRe.MatchString(strings.TrimPrefix(u.Path, "/embed/")):
			return "youtube", "video", strings.TrimPrefix(u.Path, "/embed/"), nil
		case strings.HasPrefix(u.Path, "/browse/"):
			return "", "", "", errMusicBrowse
		}
	case "youtu.be":
		if id := strings.TrimPrefix(u.Path, "/"); youtubeIDRe.MatchString(id) {
			return "youtube", "video", id, nil
		}
	case "spotify.link", "spotify.app.link":
		return "", "", "", errMusicShort
	}
	return "", "", "", errMusicLink
}

// musicURL is the public page for a piece of music.
func musicURL(provider, kind, id string) string {
	switch {
	case provider == "spotify":
		return "https://open.spotify.com/" + kind + "/" + id
	case kind == "playlist":
		return "https://www.youtube.com/playlist?list=" + id
	default:
		return "https://www.youtube.com/watch?v=" + id
	}
}

// musicOEmbed looks up a link's title, artist (YouTube channel) and artwork.
func (m *metaClient) musicOEmbed(ctx context.Context, provider, page string) (title, artist, thumb string, err error) {
	endpoint := "https://www.youtube.com/oembed?format=json&url="
	if provider == "spotify" {
		endpoint = "https://open.spotify.com/oembed?url="
	}
	var resp struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := m.getJSON(ctx, endpoint+url.QueryEscape(page), &resp); err != nil {
		return "", "", "", err
	}
	if strings.TrimSpace(resp.Title) == "" {
		return "", "", "", errNotFound
	}
	if !strings.HasPrefix(resp.ThumbnailURL, "https://") {
		resp.ThumbnailURL = ""
	}
	title, _ = cleanText(resp.Title, 200)
	artist, _ = cleanText(resp.AuthorName, 200)
	return title, artist, resp.ThumbnailURL, nil
}

const musicCols = `m.id, m.provider, m.kind, m.ext_id, m.title, m.artist, m.thumb`

type scanner interface{ Scan(...any) error }

func scanMusic(s scanner, extra ...any) (*Music, error) {
	var mu Music
	if err := s.Scan(append([]any{&mu.ID, &mu.Provider, &mu.Kind, &mu.ExtID, &mu.Title, &mu.Artist, &mu.Thumb}, extra...)...); err != nil {
		return nil, err
	}
	mu.URL = musicURL(mu.Provider, mu.Kind, mu.ExtID)
	return &mu, nil
}

func (a *App) getMusic(where string, args ...any) (*Music, error) {
	mu, err := scanMusic(a.db.QueryRow(`SELECT `+musicCols+` FROM music m `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mu, err
}

// handleResolveMusic checks a pasted link and returns its music record,
// looking it up (and saving it) the first time anyone shares it.
func (a *App) handleResolveMusic(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	provider, kind, id, err := parseMusicURL(req.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if mu, err := a.getMusic(`WHERE m.provider = ? AND m.kind = ? AND m.ext_id = ?`, provider, kind, id); err != nil {
		serverErr(w, err)
		return
	} else if mu != nil {
		writeJSON(w, http.StatusOK, mu)
		return
	}

	key := "music:" + strconv.FormatInt(u.ID, 10)
	if !a.lookups.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "that's a lot of new links; give it a few minutes")
		return
	}
	a.lookups.fail(key) // counts lookups, successful or not

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	page := musicURL(provider, kind, id)
	title, artist, thumb, err := a.meta.musicOEmbed(ctx, provider, page)
	if err != nil {
		log.Printf("music lookup %s: %v", page, err)
		name := map[string]string{"spotify": "Spotify", "youtube": "YouTube"}[provider]
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("couldn't find that on %s; is it public?", name))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO music(provider, kind, ext_id, title, artist, thumb, added_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(provider, kind, ext_id) DO NOTHING`,
		provider, kind, id, title, artist, thumb, u.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	mu, err := a.getMusic(`WHERE m.provider = ? AND m.kind = ? AND m.ext_id = ?`, provider, kind, id)
	if err != nil || mu == nil {
		serverErr(w, errors.Join(err, errors.New("music not saved")))
		return
	}
	writeJSON(w, http.StatusCreated, mu)
}

// gameMusic is a game's soundtrack, in the order it was added.
func (a *App) gameMusic(gameID int64) ([]Music, error) {
	rows, err := a.db.Query(`SELECT `+musicCols+` FROM game_music gm JOIN music m ON m.id = gm.music_id
		WHERE gm.game_id = ? ORDER BY gm.position, gm.created_at`, gameID)
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

// handleAddGameMusic attaches music to a game (board editors only).
func (a *App) handleAddGameMusic(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
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
		writeErr(w, http.StatusBadRequest, "music not found")
		return
	}
	if full, err := a.atLimit(maxMusicPerGame, `SELECT COUNT(*) FROM game_music WHERE game_id = ?`, g.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a game can have up to %d songs or playlists", maxMusicPerGame))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO game_music(game_id, music_id, added_by, position, created_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM game_music WHERE game_id = ?), ?)
		ON CONFLICT(game_id, music_id) DO NOTHING`, g.ID, req.MusicID, u.ID, g.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

func (a *App) handleRemoveGameMusic(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	musicID, err := strconv.ParseInt(r.PathValue("musicId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM game_music WHERE game_id = ? AND music_id = ?`, g.ID, musicID); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

// MusicShare is one time someone shared music: in a chat, or on a game.
type MusicShare struct {
	Where     string `json:"where"` // chat | game
	At        int64  `json:"at"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	BoardID   int64  `json:"boardId,omitempty"` // a board's chat, or the game's board
	BoardName string `json:"boardName,omitempty"`
	GameID    int64  `json:"gameId,omitempty"`
	GameTitle string `json:"gameTitle,omitempty"`
	Music     Music  `json:"music"`
}

// handleMusicShares lists recent music shares for the Music page.
func (a *App) handleMusicShares(w http.ResponseWriter, r *http.Request, u *User) {
	rows, err := a.db.Query(`
		SELECT 'chat', p.created_at, u.id, u.username, COALESCE(p.board_id, 0), COALESCE(b.name, ''), 0, '', ` + musicCols + `
		FROM posts p JOIN users u ON u.id = p.user_id JOIN music m ON m.id = p.music_id
		LEFT JOIN boards b ON b.id = p.board_id
		UNION ALL
		SELECT 'game', gm.created_at, COALESCE(u.id, 0), COALESCE(u.username, ''), g.board_id, b.name, g.id, g.title, ` + musicCols + `
		FROM game_music gm JOIN music m ON m.id = gm.music_id JOIN games g ON g.id = gm.game_id
		JOIN boards b ON b.id = g.board_id LEFT JOIN users u ON u.id = gm.added_by
		ORDER BY 2 DESC LIMIT 200`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []MusicShare{}
	for rows.Next() {
		var s MusicShare
		if err := rows.Scan(&s.Where, &s.At, &s.UserID, &s.Username, &s.BoardID, &s.BoardName, &s.GameID, &s.GameTitle,
			&s.Music.ID, &s.Music.Provider, &s.Music.Kind, &s.Music.ExtID, &s.Music.Title, &s.Music.Artist, &s.Music.Thumb); err != nil {
			serverErr(w, err)
			return
		}
		s.Music.URL = musicURL(s.Music.Provider, s.Music.Kind, s.Music.ExtID)
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// sweepMusic deletes looked-up music nobody ended up sharing (after a day).
func (a *App) sweepMusic() {
	if _, err := a.db.Exec(`DELETE FROM music WHERE created_at < ?
		AND id NOT IN (SELECT music_id FROM posts WHERE music_id IS NOT NULL)
		AND id NOT IN (SELECT music_id FROM game_music)
		AND id NOT IN (SELECT music_id FROM playlist_items)`, now()-86400); err != nil {
		log.Printf("sweep music: %v", err)
	}
}
