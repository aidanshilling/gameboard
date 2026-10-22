package main

import (
	"context"
	"database/sql"
	"encoding/json"
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

// Movies (and series), looked up on OMDb by title or IMDb id. Like music,
// they're shared in chat, put on games and collected into playlists, but
// there's nothing to play: each links out to IMDb and to where to watch it.
// OMDb needs an API key (OMDB_API_KEY); without one, movies are switched off.

const maxMoviesPerGame = 20

var errMoviesOff = errors.New("movies aren't set up yet: the server needs an OMDb API key (OMDB_API_KEY)")

type Movie struct {
	ID     int64  `json:"id"`
	IMDbID string `json:"imdbId"`
	Title  string `json:"title"`
	Year   string `json:"year,omitempty"`
	Kind   string `json:"kind"` // movie | series | episode
	Poster string `json:"poster,omitempty"`
	MovieDetails
	URL string `json:"url"` // its IMDb page
}

// MovieDetails are the extras kept as JSON in movies.details.
type MovieDetails struct {
	Rated          string `json:"rated,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	Genre          string `json:"genre,omitempty"`
	Director       string `json:"director,omitempty"`
	Actors         string `json:"actors,omitempty"`
	Plot           string `json:"plot,omitempty"`
	IMDbRating     string `json:"imdbRating,omitempty"`
	RottenTomatoes string `json:"rottenTomatoes,omitempty"`
	Metascore      string `json:"metascore,omitempty"`
}

// MovieHit is a search result, not yet saved.
type MovieHit struct {
	IMDbID string `json:"imdbId"`
	Title  string `json:"title"`
	Year   string `json:"year,omitempty"`
	Kind   string `json:"kind"`
	Poster string `json:"poster,omitempty"`
}

var (
	imdbIDRe  = regexp.MustCompile(`^tt\d{7,10}$`)
	imdbURLRe = regexp.MustCompile(`^https?://(?:www\.|m\.)?imdb\.com/(?:[a-z]{2}/)?title/(tt\d{7,10})(?:[/?#]|$)`)
)

// imdbIDFrom pulls an IMDb id out of a pasted id or IMDb link.
func imdbIDFrom(s string) string {
	s = strings.TrimSpace(s)
	if imdbIDRe.MatchString(s) {
		return s
	}
	if m := imdbURLRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func imdbURL(id string) string { return "https://www.imdb.com/title/" + id + "/" }

// omdbText cleans an OMDb field: "N/A" means nothing.
func omdbText(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "N/A" {
		return ""
	}
	s, _ = cleanText(s, max)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func omdbPoster(s string) string {
	if strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

func (m *metaClient) moviesEnabled() bool { return m.omdbKey != "" }

// omdbGet asks OMDb something. Its answers carry Response "False" and an
// Error for things like "Movie not found!". The API key never appears in
// the errors this returns.
func (m *metaClient) omdbGet(ctx context.Context, params url.Values, out any) error {
	if !m.moviesEnabled() {
		return errMoviesOff
	}
	params.Set("apikey", m.omdbKey)
	base := m.omdbBase
	if base == "" {
		base = "https://www.omdbapi.com/"
	}
	err := m.getJSON(ctx, base+"?"+params.Encode(), out)
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), m.omdbKey, "<key>"))
	}
	return nil
}

type omdbStatus struct {
	Response string `json:"Response"`
	Error    string `json:"Error"`
}

// movieSearch finds movies and series by title.
func (m *metaClient) movieSearch(ctx context.Context, q string) ([]MovieHit, error) {
	var resp struct {
		omdbStatus
		Search []struct {
			Title  string `json:"Title"`
			Year   string `json:"Year"`
			IMDbID string `json:"imdbID"`
			Type   string `json:"Type"`
			Poster string `json:"Poster"`
		} `json:"Search"`
	}
	if err := m.omdbGet(ctx, url.Values{"s": {q}}, &resp); err != nil {
		return nil, err
	}
	hits := []MovieHit{}
	if resp.Response != "True" {
		// "Movie not found!" and "Too many results." just mean nothing to show.
		if e := resp.Error; e != "" && !strings.Contains(e, "not found") && !strings.Contains(e, "Too many") {
			return nil, fmt.Errorf("omdb: %s", e)
		}
		return hits, nil
	}
	for _, r := range resp.Search {
		if !imdbIDRe.MatchString(r.IMDbID) || (r.Type != "movie" && r.Type != "series") {
			continue
		}
		hits = append(hits, MovieHit{IMDbID: r.IMDbID, Title: omdbText(r.Title, 200), Year: omdbText(r.Year, 20), Kind: r.Type, Poster: omdbPoster(r.Poster)})
	}
	return hits, nil
}

// movieDetails looks one movie up by IMDb id.
func (m *metaClient) movieDetails(ctx context.Context, id string) (*Movie, error) {
	var r struct {
		omdbStatus
		Title, Year, Rated, Runtime, Genre, Director, Actors, Plot, Poster, Metascore, Type string
		IMDbRating                                                                          string `json:"imdbRating"`
		IMDbID                                                                              string `json:"imdbID"`
		Ratings                                                                             []struct {
			Source, Value string
		}
	}
	if err := m.omdbGet(ctx, url.Values{"i": {id}, "plot": {"short"}}, &r); err != nil {
		return nil, err
	}
	if r.Response != "True" || r.IMDbID != id {
		return nil, errNotFound
	}
	mv := &Movie{
		IMDbID: id, Title: omdbText(r.Title, 200), Year: omdbText(r.Year, 20), Kind: r.Type, Poster: omdbPoster(r.Poster),
		MovieDetails: MovieDetails{
			Rated: omdbText(r.Rated, 20), Runtime: omdbText(r.Runtime, 20), Genre: omdbText(r.Genre, 200),
			Director: omdbText(r.Director, 300), Actors: omdbText(r.Actors, 300), Plot: omdbText(r.Plot, 1000),
			IMDbRating: omdbText(r.IMDbRating, 10), Metascore: omdbText(r.Metascore, 10),
		},
	}
	for _, rt := range r.Ratings {
		if rt.Source == "Rotten Tomatoes" {
			mv.RottenTomatoes = omdbText(rt.Value, 10)
		}
	}
	if mv.Kind != "series" && mv.Kind != "episode" {
		mv.Kind = "movie"
	}
	if mv.Title == "" {
		return nil, errNotFound
	}
	return mv, nil
}

const movieCols = `mv.id, mv.imdb_id, mv.title, mv.year, mv.kind, mv.poster, mv.details`

func scanMovie(s scanner, extra ...any) (*Movie, error) {
	var mv Movie
	var details string
	if err := s.Scan(append([]any{&mv.ID, &mv.IMDbID, &mv.Title, &mv.Year, &mv.Kind, &mv.Poster, &details}, extra...)...); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(details), &mv.MovieDetails)
	mv.URL = imdbURL(mv.IMDbID)
	return &mv, nil
}

func (a *App) getMovie(where string, args ...any) (*Movie, error) {
	mv, err := scanMovie(a.db.QueryRow(`SELECT `+movieCols+` FROM movies mv `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mv, err
}

func (a *App) queryMovies(q string, args ...any) ([]Movie, error) {
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Movie{}
	for rows.Next() {
		mv, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *mv)
	}
	return list, rows.Err()
}

// movieError writes a lookup failure the way people should see it.
func movieError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMoviesOff):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errNotFound):
		writeErr(w, http.StatusBadRequest, "couldn't find that on OMDb")
	default:
		log.Printf("movie lookup: %v", err)
		writeErr(w, http.StatusBadGateway, "OMDb didn't answer; try again in a moment")
	}
}

// handleMovieSearch searches OMDb by title, or looks up a pasted IMDb link or id.
func (a *App) handleMovieSearch(w http.ResponseWriter, r *http.Request, u *User) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 || len(q) > 200 {
		writeJSON(w, http.StatusOK, []MovieHit{})
		return
	}
	if !a.meta.moviesEnabled() {
		movieError(w, errMoviesOff)
		return
	}
	key := "moviesearch:" + strconv.FormatInt(u.ID, 10)
	if !a.searches.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "that's a lot of searching; give it a few minutes")
		return
	}
	a.searches.fail(key) // counts searches
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if id := imdbIDFrom(q); id != "" {
		mv, err := a.meta.movieDetails(ctx, id)
		if err != nil {
			if errors.Is(err, errNotFound) {
				writeJSON(w, http.StatusOK, []MovieHit{})
				return
			}
			movieError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, []MovieHit{{IMDbID: id, Title: mv.Title, Year: mv.Year, Kind: mv.Kind, Poster: mv.Poster}})
		return
	}
	hits, err := a.meta.movieSearch(ctx, q)
	if err != nil {
		movieError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

// handleResolveMovie saves a picked movie (looking up its details the first
// time anyone picks it) and returns it.
func (a *App) handleResolveMovie(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		IMDbID string `json:"imdbId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id := imdbIDFrom(req.IMDbID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "that isn't an IMDb id or link")
		return
	}
	if mv, err := a.getMovie(`WHERE mv.imdb_id = ?`, id); err != nil {
		serverErr(w, err)
		return
	} else if mv != nil {
		writeJSON(w, http.StatusOK, mv)
		return
	}
	key := "movie:" + strconv.FormatInt(u.ID, 10)
	if !a.lookups.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "that's a lot of new movies; give it a few minutes")
		return
	}
	a.lookups.fail(key)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	mv, err := a.meta.movieDetails(ctx, id)
	if err != nil {
		movieError(w, err)
		return
	}
	details, _ := json.Marshal(mv.MovieDetails)
	if _, err := a.db.Exec(`INSERT INTO movies(imdb_id, title, year, kind, poster, details, added_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(imdb_id) DO NOTHING`,
		id, mv.Title, mv.Year, mv.Kind, mv.Poster, string(details), u.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	saved, err := a.getMovie(`WHERE mv.imdb_id = ?`, id)
	if err != nil || saved == nil {
		serverErr(w, errors.Join(err, errors.New("movie not saved")))
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// gameMovies are a game's movies, in the order they were added.
func (a *App) gameMovies(gameID int64) ([]Movie, error) {
	return a.queryMovies(`SELECT `+movieCols+` FROM game_movies gm JOIN movies mv ON mv.id = gm.movie_id
		WHERE gm.game_id = ? ORDER BY gm.position, gm.created_at`, gameID)
}

// handleAddGameMovie puts a movie on a game (board editors only).
func (a *App) handleAddGameMovie(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	var req struct {
		MovieID int64 `json:"movieId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if mv, err := a.getMovie(`WHERE mv.id = ?`, req.MovieID); err != nil {
		serverErr(w, err)
		return
	} else if mv == nil {
		writeErr(w, http.StatusBadRequest, "movie not found")
		return
	}
	if full, err := a.atLimit(maxMoviesPerGame, `SELECT COUNT(*) FROM game_movies WHERE game_id = ?`, g.ID); err != nil {
		serverErr(w, err)
		return
	} else if full {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a game can have up to %d movies", maxMoviesPerGame))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO game_movies(game_id, movie_id, added_by, position, created_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM game_movies WHERE game_id = ?), ?)
		ON CONFLICT(game_id, movie_id) DO NOTHING`, g.ID, req.MovieID, u.ID, g.ID, now()); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

func (a *App) handleRemoveGameMovie(w http.ResponseWriter, r *http.Request, u *User) {
	g := a.loadEditableGame(w, r, u)
	if g == nil {
		return
	}
	movieID, err := strconv.ParseInt(r.PathValue("movieId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM game_movies WHERE game_id = ? AND movie_id = ?`, g.ID, movieID); err != nil {
		serverErr(w, err)
		return
	}
	a.touchBoard(g.BoardID)
	a.writeGameDetail(w, http.StatusOK, g.ID)
}

// MovieShare is one time someone shared a movie: in a chat, or on a game.
type MovieShare struct {
	Where     string `json:"where"` // chat | game
	At        int64  `json:"at"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	BoardID   int64  `json:"boardId,omitempty"`
	BoardName string `json:"boardName,omitempty"`
	GameID    int64  `json:"gameId,omitempty"`
	GameTitle string `json:"gameTitle,omitempty"`
	Movie     Movie  `json:"movie"`
}

// handleMovieShares lists recent movie shares for the Movies page, and
// whether movies are switched on.
func (a *App) handleMovieShares(w http.ResponseWriter, r *http.Request, u *User) {
	// The movie's own columns come first (that's what scanMovie reads), then where it was shared.
	rows, err := a.db.Query(`
		SELECT ` + movieCols + `, 'chat', p.created_at, u.id, u.username, COALESCE(p.board_id, 0), COALESCE(b.name, ''), 0, ''
		FROM posts p JOIN users u ON u.id = p.user_id JOIN movies mv ON mv.id = p.movie_id
		LEFT JOIN boards b ON b.id = p.board_id
		UNION ALL
		SELECT ` + movieCols + `, 'game', gm.created_at, COALESCE(u.id, 0), COALESCE(u.username, ''), g.board_id, b.name, g.id, g.title
		FROM game_movies gm JOIN movies mv ON mv.id = gm.movie_id JOIN games g ON g.id = gm.game_id
		JOIN boards b ON b.id = g.board_id LEFT JOIN users u ON u.id = gm.added_by
		ORDER BY 9 DESC LIMIT 200`)
	if err != nil {
		serverErr(w, err)
		return
	}
	defer rows.Close()
	list := []MovieShare{}
	for rows.Next() {
		var s MovieShare
		mv, err := scanMovie(rows, &s.Where, &s.At, &s.UserID, &s.Username, &s.BoardID, &s.BoardName, &s.GameID, &s.GameTitle)
		if err != nil {
			serverErr(w, err)
			return
		}
		s.Movie = *mv
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": a.meta.moviesEnabled(), "shares": list})
}

// sweepMovies deletes looked-up movies nobody ended up sharing (after a day).
func (a *App) sweepMovies() {
	if _, err := a.db.Exec(`DELETE FROM movies WHERE created_at < ?
		AND id NOT IN (SELECT movie_id FROM posts WHERE movie_id IS NOT NULL)
		AND id NOT IN (SELECT movie_id FROM game_movies)
		AND id NOT IN (SELECT movie_id FROM playlist_movies)`, now()-86400); err != nil {
		log.Printf("sweep movies: %v", err)
	}
}
