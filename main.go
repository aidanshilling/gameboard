package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web
var webFS embed.FS

type Config struct {
	Addr           string
	DBPath         string
	AllowSignups   bool   // whether people can request accounts (admins approve them)
	CookieSecure   string // auto | true | false
	TrustProxy     bool   // trust X-Forwarded-For / X-Forwarded-Proto
	TwitchClientID string // IGDB uses Twitch app credentials
	TwitchSecret   string
	SteamCountry   string // country code for Steam prices
}

type App struct {
	db      *sql.DB
	cfg     Config
	limiter *limiter // failed logins
	signups *limiter // sign-up requests per IP
	posts   *limiter // chat posts per user
	lookups *limiter // new music links looked up per user
	meta    *metaClient
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	return Config{
		Addr:           env("ADDR", ":8080"),
		DBPath:         env("DB_PATH", "gameboard.db"),
		AllowSignups:   env("ALLOW_SIGNUPS", "true") != "false",
		CookieSecure:   strings.ToLower(env("COOKIE_SECURE", "auto")),
		TrustProxy:     env("TRUST_PROXY", "false") == "true",
		TwitchClientID: os.Getenv("TWITCH_CLIENT_ID"),
		TwitchSecret:   os.Getenv("TWITCH_CLIENT_SECRET"),
		SteamCountry:   env("STEAM_COUNTRY", "US"),
	}
}

func main() {
	cfg := loadConfig()
	db, err := openDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if len(os.Args) > 1 {
		if err := runCommand(db, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	app := &App{
		db:      db,
		cfg:     cfg,
		limiter: newLimiter(10, 15*time.Minute),
		signups: newLimiter(5, time.Hour),
		posts:   newLimiter(15, 5*time.Minute),
		lookups: newLimiter(30, 5*time.Minute),
		meta:    newMetaClient(cfg),
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.sweepExpired(ctx)
	go app.checkPricesLoop(ctx)

	go func() {
		log.Printf("listening on %s (db: %s, igdb enabled: %v)", cfg.Addr, cfg.DBPath, app.meta.igdbEnabled())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	mux.HandleFunc("GET /api/setup", a.handleSetup)
	mux.HandleFunc("POST /api/register", a.handleRegister)
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("POST /api/logout", a.handleLogout)
	mux.HandleFunc("GET /api/me", a.auth(a.handleMe))
	mux.HandleFunc("PUT /api/me/admin-mode", a.admin(a.handleAdminMode))
	mux.HandleFunc("PUT /api/me/theme", a.auth(a.handleSetTheme))
	mux.HandleFunc("GET /api/themes", a.auth(a.handleListThemes))
	mux.HandleFunc("POST /api/themes", a.auth(a.handleCreateTheme))
	mux.HandleFunc("PATCH /api/themes/{id}", a.auth(a.handleUpdateTheme))
	mux.HandleFunc("DELETE /api/themes/{id}", a.auth(a.handleDeleteTheme))
	mux.HandleFunc("PUT /api/me/avatar", a.auth(a.handleSetAvatar))
	mux.HandleFunc("DELETE /api/me/avatar", a.auth(a.handleDeleteAvatar))
	mux.HandleFunc("GET /api/avatars/{id}", a.auth(a.handleGetAvatar))
	mux.HandleFunc("DELETE /api/admin/users/{id}/avatar", a.admin(a.handleAdminDeleteAvatar))
	mux.HandleFunc("POST /api/password", a.auth(a.handleChangePassword))

	mux.HandleFunc("GET /api/boards", a.auth(a.handleListBoards))
	mux.HandleFunc("POST /api/boards", a.auth(a.handleCreateBoard))
	mux.HandleFunc("GET /api/boards/{id}", a.auth(a.handleGetBoard))
	mux.HandleFunc("PATCH /api/boards/{id}", a.auth(a.handleUpdateBoard))
	mux.HandleFunc("DELETE /api/boards/{id}", a.auth(a.handleDeleteBoard))
	mux.HandleFunc("POST /api/boards/{id}/games", a.auth(a.handleAddGame))
	mux.HandleFunc("POST /api/boards/{id}/columns", a.auth(a.handleAddColumn))
	mux.HandleFunc("PUT /api/boards/{id}/columns/order", a.auth(a.handleReorderColumns))
	mux.HandleFunc("POST /api/boards/{id}/members", a.auth(a.handleAddMember))
	mux.HandleFunc("DELETE /api/boards/{id}/members/{userId}", a.auth(a.handleRemoveMember))
	mux.HandleFunc("POST /api/boards/{id}/requests", a.auth(a.handleRequestJoin))
	mux.HandleFunc("POST /api/boards/{id}/requests/{userId}/approve", a.auth(a.handleApproveJoin))
	mux.HandleFunc("DELETE /api/boards/{id}/requests/{userId}", a.auth(a.handleDeleteJoin))
	mux.HandleFunc("GET /api/users", a.auth(a.handleListUsers))
	mux.HandleFunc("PATCH /api/columns/{id}", a.auth(a.handleUpdateColumn))
	mux.HandleFunc("DELETE /api/columns/{id}", a.auth(a.handleDeleteColumn))

	mux.HandleFunc("GET /api/games/{id}", a.auth(a.handleGetGame))
	mux.HandleFunc("PATCH /api/games/{id}", a.auth(a.handleUpdateGame))
	mux.HandleFunc("DELETE /api/games/{id}", a.auth(a.handleDeleteGame))
	mux.HandleFunc("PUT /api/games/{id}/links", a.auth(a.handleSetLinks))
	mux.HandleFunc("POST /api/games/{id}/refresh", a.auth(a.handleRefreshGame))
	mux.HandleFunc("PUT /api/games/{id}/interest", a.auth(a.handleSetInterest))
	mux.HandleFunc("POST /api/games/{id}/notes", a.auth(a.handleAddNote))
	mux.HandleFunc("PATCH /api/notes/{id}", a.auth(a.handleUpdateNote))
	mux.HandleFunc("DELETE /api/notes/{id}", a.auth(a.handleDeleteNote))

	mux.HandleFunc("GET /api/search", a.auth(a.handleSearch))

	mux.HandleFunc("GET /api/feed", a.auth(a.handleFeed))
	mux.HandleFunc("POST /api/feed", a.auth(a.handleAddPost))
	mux.HandleFunc("DELETE /api/feed/{id}", a.auth(a.handleDeletePost))
	mux.HandleFunc("GET /api/feed/games", a.auth(a.handleFeedGames))
	mux.HandleFunc("POST /api/music/resolve", a.auth(a.handleResolveMusic))
	mux.HandleFunc("GET /api/music/shares", a.auth(a.handleMusicShares))
	mux.HandleFunc("POST /api/games/{id}/music", a.auth(a.handleAddGameMusic))
	mux.HandleFunc("DELETE /api/games/{id}/music/{musicId}", a.auth(a.handleRemoveGameMusic))
	mux.HandleFunc("GET /api/highlights", a.auth(a.handleHighlights))
	mux.HandleFunc("GET /api/games/{id}/everywhere", a.auth(a.handleGameEverywhere))
	mux.HandleFunc("PUT /api/games/{id}/everywhere/interest", a.auth(a.handleSetInterestEverywhere))
	mux.HandleFunc("GET /api/playlists", a.auth(a.handleListPlaylists))
	mux.HandleFunc("POST /api/playlists", a.auth(a.handleCreatePlaylist))
	mux.HandleFunc("GET /api/playlists/{id}", a.auth(a.handleGetPlaylist))
	mux.HandleFunc("PATCH /api/playlists/{id}", a.auth(a.handleRenamePlaylist))
	mux.HandleFunc("DELETE /api/playlists/{id}", a.auth(a.handleDeletePlaylist))
	mux.HandleFunc("POST /api/playlists/{id}/items", a.auth(a.handleAddPlaylistItem))
	mux.HandleFunc("PUT /api/playlists/{id}/items", a.auth(a.handleOrderPlaylist))
	mux.HandleFunc("DELETE /api/playlists/{id}/items/{musicId}", a.auth(a.handleRemovePlaylistItem))
	mux.HandleFunc("POST /api/games/{id}/playlists", a.auth(a.handleAddGamePlaylist))
	mux.HandleFunc("DELETE /api/games/{id}/playlists/{playlistId}", a.auth(a.handleRemoveGamePlaylist))
	mux.HandleFunc("GET /api/boards/{id}/feed", a.auth(a.handleBoardFeed))
	mux.HandleFunc("POST /api/boards/{id}/feed", a.auth(a.handleAddBoardPost))

	mux.HandleFunc("GET /api/ticker", a.auth(a.handleGetTicker))
	mux.HandleFunc("POST /api/ticker", a.auth(a.handleAddTicker))
	mux.HandleFunc("DELETE /api/ticker/{id}", a.auth(a.handleDeleteTicker))
	mux.HandleFunc("GET /api/admin/ticker", a.admin(a.handleAdminTicker))
	mux.HandleFunc("POST /api/admin/ticker/defaults", a.admin(a.handleAdminAddDefault))
	mux.HandleFunc("PATCH /api/admin/ticker/defaults/{id}", a.admin(a.handleAdminUpdateDefault))
	mux.HandleFunc("DELETE /api/admin/ticker/defaults/{id}", a.admin(a.handleAdminDeleteDefault))
	mux.HandleFunc("DELETE /api/admin/ticker/messages", a.admin(a.handleAdminClearTicker))

	mux.HandleFunc("GET /api/admin/pending", a.admin(a.handleAdminListPending))
	mux.HandleFunc("POST /api/admin/pending/{id}/approve", a.admin(a.handleAdminApprove))
	mux.HandleFunc("DELETE /api/admin/pending/{id}", a.admin(a.handleAdminReject))
	mux.HandleFunc("GET /api/admin/users", a.admin(a.handleAdminListUsers))
	mux.HandleFunc("POST /api/admin/users", a.admin(a.handleAdminCreateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}", a.admin(a.handleAdminDeleteUser))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", a.admin(a.handleAdminResetPassword))

	return a.middleware(mux)
}

// contentSecurityPolicy only lets the page run its own code, plus YouTube's
// official player script and frames for the music player (loaded only when
// someone presses play; Spotify's player runs in spotify-player.html). blob:
// lets the picture cropper show a chosen file before it's uploaded.
var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"img-src 'self' data: blob: https:",
	"style-src 'self'",
	"script-src 'self' https://www.youtube.com/iframe_api https://www.youtube.com/s/player/",
	"frame-src 'self' https://www.youtube-nocookie.com https://www.youtube.com",
	"connect-src 'self'",
	"frame-ancestors 'none'",
	"base-uri 'self'",
	"form-action 'self'",
}, "; ")

// spotifyPlayerPolicy is for spotify-player.html, which hosts Spotify's embed
// controller. That script needs eval, so it gets a page of its own with no
// user content in it, which only this site may frame.
const spotifyPlayerPage = "/spotify-player.html"

var spotifyPlayerPolicy = strings.Join([]string{
	"default-src 'none'",
	"script-src 'self' 'unsafe-eval' https://open.spotify.com/embed/ https://embed-cdn.spotifycdn.com/_next/static/",
	"frame-src https://open.spotify.com",
	"frame-ancestors 'self'",
	"base-uri 'none'",
	"form-action 'none'",
}, "; ")

// middleware sets security headers and protects state-changing API calls
// against CSRF: they must carry a custom header (which cross-site pages can't
// send without a CORS preflight we never approve) and a matching Origin.
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		if r.URL.Path == spotifyPlayerPage {
			h.Set("Content-Security-Policy", spotifyPlayerPolicy)
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		// Other sites see only the origin, not which page; YouTube's player
		// refuses to play without it.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-cache")
			next.ServeHTTP(w, r)
			return
		}

		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Requested-With") != "gameboard" {
				writeErr(w, http.StatusForbidden, "missing request header")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeErr(w, http.StatusForbidden, "cross-origin request rejected")
					return
				}
			}
			limit := int64(1 << 20)
			if r.URL.Path == "/api/me/avatar" {
				limit = avatarMaxUpload // pictures are bigger than JSON
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// sweepExpired hourly deletes expired sessions, last week's ticker messages
// and looked-up music nobody shared.
func (a *App) sweepExpired(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if _, err := a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now()); err != nil {
			log.Printf("sweep sessions: %v", err)
		}
		a.sweepTicker()
		a.sweepMusic()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

const usage = `usage:
  gameboard                           run the web server
  gameboard create-admin <username>   create a new admin account (prints its password)
  gameboard make-admin <username>     give an existing user admin rights
  gameboard remove-admin <username>   take admin rights away from a user
  gameboard reset-password <username> set a new random password for a user
  gameboard list-users                show users and pending sign-ups`

func runCommand(db *sql.DB, args []string) error {
	switch args[0] {
	case "create-admin":
		if len(args) != 2 {
			return errors.New(usage)
		}
		name := args[1]
		if !usernameRe.MatchString(name) {
			return errors.New("username must be 3-32 characters: letters, numbers, . _ -")
		}
		if taken, err := usernameTaken(db, name); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("%q already exists as a user or pending sign-up (use make-admin for an existing user)", name)
		}
		pw := randomPassword(16)
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO users(username, password_hash, is_admin, created_at) VALUES (?, ?, 1, ?)`,
			name, string(hash), now()); err != nil {
			return err
		}
		fmt.Printf("Created admin %s with password: %s\nLog in and change it from the Password button.\n", name, pw)
		return nil
	case "remove-admin":
		if len(args) != 2 {
			return errors.New(usage)
		}
		res, err := db.Exec(`UPDATE users SET is_admin = 0 WHERE username = ?`, args[1])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no user named %q", args[1])
		}
		fmt.Printf("%s is no longer an admin\n", args[1])
		return nil
	case "list-users":
		rows, err := db.Query(`SELECT username, is_admin, created_at FROM users ORDER BY is_admin DESC, username`)
		if err != nil {
			return err
		}
		fmt.Println("Users:")
		for rows.Next() {
			var name string
			var admin bool
			var created int64
			if err := rows.Scan(&name, &admin, &created); err != nil {
				rows.Close()
				return err
			}
			role := "user"
			if admin {
				role = "admin"
			}
			fmt.Printf("  %-32s %-6s joined %s\n", name, role, time.Unix(created, 0).Format("2006-01-02"))
		}
		rows.Close()
		rows, err = db.Query(`SELECT username, message, created_at FROM pending_users ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Println("Pending sign-ups:")
		for rows.Next() {
			var name, msg string
			var created int64
			if err := rows.Scan(&name, &msg, &created); err != nil {
				return err
			}
			fmt.Printf("  %-32s requested %s  %s\n", name, time.Unix(created, 0).Format("2006-01-02"), msg)
		}
		return rows.Err()
	case "reset-password":
		if len(args) != 2 {
			return errors.New(usage)
		}
		pw := randomPassword(16)
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
		if err != nil {
			return err
		}
		res, err := db.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, string(hash), args[1])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no user named %q", args[1])
		}
		if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, args[1]); err != nil {
			return err
		}
		fmt.Printf("New password for %s: %s\n", args[1], pw)
		return nil
	case "make-admin":
		if len(args) != 2 {
			return errors.New(usage)
		}
		res, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE username = ?`, args[1])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no user named %q", args[1])
		}
		fmt.Printf("%s is now an admin\n", args[1])
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func randomPassword(n int) string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic(err)
		}
		b[i] = chars[x.Int64()]
	}
	return string(b)
}
