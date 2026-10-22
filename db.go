package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);

-- Sign-up requests live here, completely separate from users, until an
-- admin approves them. A pending row grants no access of any kind.
CREATE TABLE IF NOT EXISTS pending_users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	message       TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS boards (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
	personal    INTEGER NOT NULL DEFAULT 0, -- nobody can request to join
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);

-- Profile pictures: a small re-encoded image per user (JPEG for photos, PNG
-- at native size for pixel art). version is a timestamp in milliseconds,
-- used in the picture's URL to bust caches.
CREATE TABLE IF NOT EXISTS avatars (
	user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	image   BLOB NOT NULL,
	version INTEGER NOT NULL,
	pixel   INTEGER NOT NULL DEFAULT 0
);

-- People's own colour themes. vars is a JSON object of colour settings.
CREATE TABLE IF NOT EXISTS themes (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	vars       TEXT NOT NULL,
	shared     INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

-- Spotify / YouTube (Music) links, as checked and looked up by the server.
-- Shared in chat (posts.music_id) and attached to games (game_music).
CREATE TABLE IF NOT EXISTS music (
	id         INTEGER PRIMARY KEY,
	provider   TEXT NOT NULL,             -- spotify | youtube
	kind       TEXT NOT NULL,             -- track, album, playlist, video...
	ext_id     TEXT NOT NULL,             -- the service's own id
	title      TEXT NOT NULL,
	artist     TEXT NOT NULL DEFAULT '',
	thumb      TEXT NOT NULL DEFAULT '',
	added_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (provider, kind, ext_id)
);

-- A game's soundtrack.
CREATE TABLE IF NOT EXISTS game_music (
	game_id    INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	music_id   INTEGER NOT NULL REFERENCES music(id) ON DELETE CASCADE,
	added_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	position   REAL NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (game_id, music_id)
);

-- People's own playlists of songs from the music table, in order.
CREATE TABLE IF NOT EXISTS playlists (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS playlists_user ON playlists(user_id);
CREATE TABLE IF NOT EXISTS playlist_items (
	playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
	music_id    INTEGER NOT NULL REFERENCES music(id) ON DELETE CASCADE,
	position    REAL NOT NULL,
	added_at    INTEGER NOT NULL,
	PRIMARY KEY (playlist_id, music_id)
);

-- Playlists on a game, next to its own songs (game_music).
CREATE TABLE IF NOT EXISTS game_playlists (
	game_id     INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
	added_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (game_id, playlist_id)
);

-- Chat posts: site-wide when board_id is NULL, otherwise that board's chat.
-- A post can point at a game on any board, or at a game from Steam/IGDB
-- (the link_* columns, looked up when it's posted).
CREATE TABLE IF NOT EXISTS posts (
	id            INTEGER PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	board_id      INTEGER REFERENCES boards(id) ON DELETE CASCADE,
	body          TEXT NOT NULL,
	game_id       INTEGER REFERENCES games(id) ON DELETE SET NULL,
	link_title    TEXT NOT NULL DEFAULT '',
	link_image    TEXT NOT NULL DEFAULT '',
	link_steam    INTEGER,
	link_igdb     INTEGER,
	link_igdb_url TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL
);

-- The footer ticker: admin-set defaults, plus people's own messages that
-- are cleared at the weekly reset.
CREATE TABLE IF NOT EXISTS ticker_defaults (
	id         INTEGER PRIMARY KEY,
	text       TEXT NOT NULL,
	position   REAL NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ticker_messages (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	text       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

-- People besides the creator who may edit a board. Everyone else can view
-- it, vote and leave notes, but not change its games or columns.
CREATE TABLE IF NOT EXISTS board_members (
	board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	added_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (board_id, user_id)
);

-- Viewers asking a board's owner to make them an editor.
CREATE TABLE IF NOT EXISTS board_join_requests (
	board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (board_id, user_id)
);

-- Each board's own columns (categories) that games are sorted into.
CREATE TABLE IF NOT EXISTS board_columns (
	id         INTEGER PRIMARY KEY,
	board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	color      TEXT NOT NULL DEFAULT 'periwinkle',
	position   REAL NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS board_columns_board ON board_columns(board_id);

CREATE TABLE IF NOT EXISTS games (
	id              INTEGER PRIMARY KEY,
	board_id        INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	title           TEXT NOT NULL,
	status          TEXT NOT NULL DEFAULT 'wishlist', -- legacy fixed column, replaced by column_id
	column_id       INTEGER REFERENCES board_columns(id) ON DELETE SET NULL,
	position        REAL NOT NULL DEFAULT 0,
	steam_appid     INTEGER,
	igdb_id         INTEGER,
	meta            TEXT NOT NULL DEFAULT '{}',
	meta_updated_at INTEGER NOT NULL DEFAULT 0,
	added_by        INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS games_board ON games(board_id);

CREATE TABLE IF NOT EXISTS notes (
	id         INTEGER PRIMARY KEY,
	game_id    INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	body       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_game ON notes(game_id);

-- level: 2 = want to play, 1 = maybe, -1 = pass
CREATE TABLE IF NOT EXISTS interests (
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	level   INTEGER NOT NULL DEFAULT 0,
	owns    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (game_id, user_id)
);
`

func openDB(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if err := addColumn(db, "users", "last_login_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn(db, "games", "column_id", "INTEGER REFERENCES board_columns(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if err := addColumn(db, "boards", "personal", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn(db, "users", "admin_mode", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// "" (default), "preset:<name>" or "custom:<theme id>".
	if err := addColumn(db, "users", "theme", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn(db, "avatars", "pixel", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// When the Steam price in meta was last checked (prices.go).
	if err := addColumn(db, "games", "price_checked_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	for _, c := range [][2]string{
		{"link_title", "TEXT NOT NULL DEFAULT ''"},
		{"link_image", "TEXT NOT NULL DEFAULT ''"},
		{"link_steam", "INTEGER"},
		{"link_igdb", "INTEGER"},
		{"link_igdb_url", "TEXT NOT NULL DEFAULT ''"},
		{"board_id", "INTEGER REFERENCES boards(id) ON DELETE CASCADE"},
		{"music_id", "INTEGER REFERENCES music(id) ON DELETE SET NULL"},
		{"playlist_id", "INTEGER REFERENCES playlists(id) ON DELETE SET NULL"},
	} {
		if err := addColumn(db, "posts", c[0], c[1]); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS posts_board ON posts(board_id, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS games_column ON games(column_id)`); err != nil {
		return err
	}
	if err := migrateLegacyColumns(db); err != nil {
		return err
	}

	// One-off steps, tracked with SQLite's user_version.
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		// Before board permissions everyone could edit every board. Keep that for
		// the people who'd actually worked on a board by making them its editors.
		if _, err := db.Exec(`INSERT OR IGNORE INTO board_members(board_id, user_id, created_at)
			SELECT DISTINCT x.board_id, x.user_id, ? FROM (
				SELECT board_id, added_by AS user_id FROM games
				UNION SELECT g.board_id, i.user_id FROM interests i JOIN games g ON g.id = i.game_id
				UNION SELECT g.board_id, n.user_id FROM notes n JOIN games g ON g.id = n.game_id
			) x JOIN boards b ON b.id = x.board_id
			WHERE x.user_id IS NOT NULL AND x.user_id != COALESCE(b.created_by, 0)`, now()); err != nil {
			return err
		}
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
	}
	if version < 2 {
		// The ticker's defaults used to be hard-coded; start from those.
		for i, text := range initialTicker {
			if _, err := db.Exec(`INSERT INTO ticker_defaults(text, position, created_at) VALUES (?, ?, ?)`, text, i+1, now()); err != nil {
				return err
			}
		}
		if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
			return err
		}
	}
	return nil
}

// Boards from before custom columns used a fixed list of statuses. Give each
// such board those statuses as real columns and put its games in them.
var legacyColumns = []struct{ status, name, color string }{
	{"wishlist", "Wishlist", "periwinkle"},
	{"buy", "Next to buy", "peach"},
	{"ready", "Ready to play", "ice"},
	{"playing", "Playing", "mint"},
	{"done", "Finished", "lilac"},
	{"shelved", "Shelved", "stone"},
}

func migrateLegacyColumns(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM boards b WHERE NOT EXISTS (SELECT 1 FROM board_columns c WHERE c.board_id = b.id)`)
	if err != nil {
		return err
	}
	var boards []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		boards = append(boards, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, boardID := range boards {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for i, lc := range legacyColumns {
			res, err := tx.Exec(`INSERT INTO board_columns(board_id, name, color, position, created_at) VALUES (?, ?, ?, ?, ?)`,
				boardID, lc.name, lc.color, i+1, now())
			if err != nil {
				tx.Rollback()
				return err
			}
			colID, _ := res.LastInsertId()
			if _, err := tx.Exec(`UPDATE games SET column_id = ? WHERE board_id = ? AND status = ? AND column_id IS NULL`,
				colID, boardID, lc.status); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	// Anything still without a column goes into its board's first one.
	_, err = db.Exec(`UPDATE games SET column_id = (SELECT c.id FROM board_columns c
		WHERE c.board_id = games.board_id ORDER BY c.position, c.id LIMIT 1) WHERE column_id IS NULL`)
	return err
}

// addColumn adds a column to an existing table, doing nothing if it's already there.
func addColumn(db *sql.DB, table, column, def string) error {
	_, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	return err
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// usernameTaken reports whether a name is used by a user or a pending sign-up.
func usernameTaken(q querier, name string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT (SELECT COUNT(*) FROM users WHERE username = ?) +
		(SELECT COUNT(*) FROM pending_users WHERE username = ?)`, name, name).Scan(&n)
	return n > 0, err
}

func now() int64 { return time.Now().Unix() }
