package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameEverywhere(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	ann := exec(`INSERT INTO users(username, password_hash, created_at) VALUES ('ann', 'x', 0)`)
	bob := exec(`INSERT INTO users(username, password_hash, created_at) VALUES ('bob', 'x', 0)`)
	b1 := exec(`INSERT INTO boards(name, created_by, created_at, updated_at) VALUES ('Zed board', ?, 0, 0)`, ann)
	b2 := exec(`INSERT INTO boards(name, created_by, created_at, updated_at) VALUES ('Alpha board', ?, 0, 0)`, bob)
	game := func(board int64, title string, steam any) int64 {
		return exec(`INSERT INTO games(board_id, title, steam_appid, meta, created_at, updated_at) VALUES (?, ?, ?, '{"links":{}}', 0, 0)`, board, title, steam)
	}
	v1 := game(b1, "Valheim", 892970)
	v2 := game(b2, "Valheim", 892970)
	other := game(b2, "Hades", 1145360)
	t1 := game(b1, "Board Game", nil)
	t2 := game(b2, " board game ", nil)
	exec(`INSERT INTO interests(game_id, user_id, level, owns) VALUES (?, ?, 2, 0), (?, ?, -1, 1)`, v1, bob, v2, bob) // bob: want here, pass + owns there
	exec(`INSERT INTO interests(game_id, user_id, level, owns) VALUES (?, ?, 1, 0)`, other, ann)

	call := func(method string, id int64, body string, u int64) GameEverywhere {
		t.Helper()
		req := httptest.NewRequest(method, "/x", strings.NewReader(body))
		req.SetPathValue("id", fmt.Sprint(id))
		rec := httptest.NewRecorder()
		if method == "GET" {
			a.handleGameEverywhere(rec, req, &User{ID: u})
		} else {
			a.handleSetInterestEverywhere(rec, req, &User{ID: u})
		}
		var out GameEverywhere
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return out
	}

	got := call("GET", v2, "", ann)
	if len(got.Copies) != 2 || got.Copies[0].BoardName != "Alpha board" {
		t.Errorf("copies = %+v, want both boards, sorted by name", got.Copies)
	}
	if len(got.Votes) != 1 || got.Votes[0].Level != 2 || !got.Votes[0].Owns {
		t.Errorf("bob's merged vote = %+v, want want-to-play and owns", got.Votes)
	}

	// ann votes from the Games page: lands on both copies of Valheim, not on Hades.
	got = call("PUT", v1, `{"level":2,"owns":false}`, ann)
	if got.Me.Level != 2 || len(got.Votes) != 2 {
		t.Errorf("after voting: me = %+v, votes = %+v", got.Me, got.Votes)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM interests WHERE user_id = ? AND game_id IN (?, ?) AND level = 2`, ann, v1, v2).Scan(&n)
	if n != 2 {
		t.Errorf("ann's vote is on %d copies, want 2", n)
	}
	db.QueryRow(`SELECT level FROM interests WHERE user_id = ? AND game_id = ?`, ann, other).Scan(&n)
	if n != 1 {
		t.Errorf("ann's Hades vote changed to %d", n)
	}
	// Clearing removes it everywhere.
	call("PUT", v2, `{"level":0,"owns":false}`, ann)
	db.QueryRow(`SELECT COUNT(*) FROM interests WHERE user_id = ? AND game_id IN (?, ?)`, ann, v1, v2).Scan(&n)
	if n != 0 {
		t.Errorf("after clearing, ann still has %d votes on Valheim", n)
	}
	// Games without ids match by title.
	if got := call("GET", t1, "", ann); len(got.Copies) != 2 {
		t.Errorf("title match found %d copies (%d, %d)", len(got.Copies), t1, t2)
	}
}
