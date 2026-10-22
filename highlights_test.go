package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSteamPricesLive asks the real Steam store; run with GAMEBOARD_LIVE=1.
func TestSteamPricesLive(t *testing.T) {
	if os.Getenv("GAMEBOARD_LIVE") == "" {
		t.Skip("set GAMEBOARD_LIVE=1 to ask Steam")
	}
	m := &metaClient{http: &http.Client{Timeout: 20 * time.Second}, country: "US"}
	// Valheim and Hades (paid), Dota 2 (free), and an id that isn't an app.
	prices, err := m.steamPrices(context.Background(), []string{"892970", "1145360", "570", "1"})
	if err != nil {
		t.Fatal(err)
	}
	for id, p := range prices {
		t.Logf("%s: %+v", id, *p)
	}
	if prices["892970"] == nil || prices["892970"].Cents <= 0 || prices["1145360"] == nil {
		t.Errorf("paid games missing a price: %+v", prices)
	}
	if prices["570"] != nil || prices["1"] != nil {
		t.Errorf("free / unknown apps should have no price: %+v", prices)
	}
}

func TestHighlights(t *testing.T) {
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
	user := func(name string) int64 {
		return exec(`INSERT INTO users(username, password_hash, created_at) VALUES (?, 'x', 0)`, name)
	}
	board := func(name string, owner int64) int64 {
		return exec(`INSERT INTO boards(name, created_by, created_at, updated_at) VALUES (?, ?, 0, 0)`, name, owner)
	}
	// price: discount percent, -1 for free, 0 for full price.
	game := func(board int64, title string, steam int64, discount int) int64 {
		meta := `{"links":{}}`
		switch {
		case discount < 0:
			meta = `{"links":{},"price":{"free":true,"cents":0}}`
		case discount >= 0:
			meta = fmt.Sprintf(`{"links":{},"headerUrl":"https://img/%d.jpg","price":{"cents":999,"final":"$9.99","initial":"$19.99","discountPercent":%d}}`, steam, discount)
		}
		return exec(`INSERT INTO games(board_id, title, steam_appid, meta, created_at, updated_at) VALUES (?, ?, ?, ?, 0, 0)`, board, title, steam, meta)
	}
	mark := func(game, user int64, level int, owns bool) {
		exec(`INSERT INTO interests(game_id, user_id, level, owns) VALUES (?, ?, ?, ?)`, game, user, level, owns)
	}

	ann, bob, cat := user("ann"), user("bob"), user("cat")
	annsBoard, bobsBoard, catsBoard := board("Ann's", ann), board("Bob's", bob), board("Cat's", cat)
	exec(`INSERT INTO board_members(board_id, user_id, created_at) VALUES (?, ?, 0)`, catsBoard, ann) // ann edits cat's board

	valheimA := game(annsBoard, "Valheim", 892970, 50) // ann's list, on sale
	valheimB := game(bobsBoard, "Valheim", 892970, 50) // same game on bob's board
	hadesA := game(annsBoard, "Hades", 1145360, 30)    // on sale, but ann owns it (on bob's copy)
	hadesB := game(bobsBoard, "Hades", 1145360, 30)    //
	celeste := game(bobsBoard, "Celeste", 504230, 75)  // not ann's board, but she wants it
	game(bobsBoard, "Stardew Valley", 413150, 20)      // not ann's board, no interest: not hers
	game(catsBoard, "Factorio", 427520, 0)             // ann's (editor) list, full price
	game(catsBoard, "Terraria", 105600, 10)            // ann's (editor) list, on sale
	game(annsBoard, "Free thing", 1, -1)               // free: never "on sale"

	mark(valheimA, bob, 2, false)
	mark(valheimB, bob, 2, false) // bob twice: counts once
	mark(valheimB, cat, 2, false)
	mark(celeste, ann, 2, false)
	mark(hadesB, ann, 0, true)
	mark(hadesA, cat, 1, true)

	rec := httptest.NewRecorder()
	a.handleHighlights(rec, httptest.NewRequest("GET", "/api/highlights", nil), &User{ID: ann})
	var got struct{ Wanted, Owned, Sales []Highlight }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	summary := func(list []Highlight) string {
		s := ""
		for _, h := range list {
			s += fmt.Sprintf("%s:%d ", h.Title, len(h.Users))
		}
		return s
	}
	if s := summary(got.Wanted); s != "Valheim:2 Celeste:1 " {
		t.Errorf("wanted = %q", s)
	}
	if s := summary(got.Owned); s != "Hades:2 " {
		t.Errorf("owned = %q", s)
	}
	titles := ""
	for _, h := range got.Sales {
		titles += fmt.Sprintf("%s-%d%% ", h.Title, h.Price.DiscountPercent)
	}
	if titles != "Celeste-75% Valheim-50% Terraria-10% " {
		t.Errorf("sales for ann = %q", titles)
	}
	for _, h := range got.Sales {
		if h.Title == "Valheim" {
			if h.BoardID != annsBoard {
				t.Errorf("Valheim should open on ann's own board, got board %d", h.BoardID)
			}
			if len(h.Users) != 2 {
				t.Errorf("Valheim wanters = %+v, want bob and cat", h.Users)
			}
		}
	}
}

func TestRank(t *testing.T) {
	steam := func(n int64) *int64 { return &n }
	mark := func(gameID, boardID int64, title string, appID *int64, userID int64, user string, wants bool) highlightGame {
		return highlightGame{id: gameID, boardID: boardID, title: title, board: "b", steam: appID,
			key: gameKey(appID, nil, title), userID: userID, username: user, wants: wants}
	}
	marks := []highlightGame{
		// Valheim on two boards: ann and bob on one copy, bob again and cat on the other.
		mark(1, 10, "Valheim", steam(892970), 1, "ann", true),
		mark(1, 10, "Valheim", steam(892970), 2, "bob", true),
		mark(2, 20, "Valheim", steam(892970), 2, "bob", true),
		mark(2, 20, "Valheim", steam(892970), 3, "cat", true),
		// A game with no ids: matched by title, case and spaces aside.
		mark(3, 10, "Board Game Night", nil, 1, "ann", true),
		mark(4, 20, " board game night", nil, 3, "cat", true),
		// Owned only, not wanted: left out.
		mark(5, 10, "Hades", steam(1145360), 1, "ann", false),
	}
	got := rank(marks, func(g highlightGame) bool { return g.wants }, 0)
	if len(got) != 2 {
		t.Fatalf("got %d games, want 2: %+v", len(got), got)
	}
	v := got[0]
	if v.Title != "Valheim" || len(v.Users) != 3 || v.Boards != 2 {
		t.Errorf("first = %s with %d people on %d boards, want Valheim with 3 on 2", v.Title, len(v.Users), v.Boards)
	}
	if v.Users[0].Username != "ann" || v.Users[2].Username != "cat" {
		t.Errorf("people not sorted by name: %+v", v.Users)
	}
	if got[1].Boards != 2 || len(got[1].Users) != 2 {
		t.Errorf("title match: %+v", got[1])
	}
	if one := rank(marks, func(g highlightGame) bool { return g.wants }, 1); len(one) != 1 {
		t.Errorf("max 1 gave %d", len(one))
	}
}
