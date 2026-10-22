package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWeekStart(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-10-19T00:00:00Z", "2026-10-19T00:00:00Z"},      // Monday midnight is the reset itself
		{"2026-10-21T20:15:00Z", "2026-10-19T00:00:00Z"},      // Wednesday
		{"2026-10-25T23:59:59Z", "2026-10-19T00:00:00Z"},      // Sunday, last second of the week
		{"2026-10-26T00:00:00Z", "2026-10-26T00:00:00Z"},      // next Monday
		{"2026-11-01T12:00:00Z", "2026-10-26T00:00:00Z"},      // across a month boundary
		{"2026-10-21T23:30:00-05:00", "2026-10-19T00:00:00Z"}, // non-UTC input (Thursday 04:30 UTC)
	}
	for _, c := range cases {
		in, _ := time.Parse(time.RFC3339, c.in)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := weekStart(in); !got.Equal(want) {
			t.Errorf("weekStart(%s) = %s, want %s", c.in, got, want)
		}
	}
}

func TestTickerWeeklyReset(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}

	defaults, err := a.tickerDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != len(initialTicker) {
		t.Fatalf("seeded %d defaults, want %d", len(defaults), len(initialTicker))
	}

	res, err := db.Exec(`INSERT INTO users(username, password_hash, created_at) VALUES ('dave', 'x', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	reset := weekStart(time.Now()).Unix()
	for _, m := range []struct {
		text string
		at   int64
	}{{"last week", reset - 1}, {"this week", reset}, {"just now", time.Now().Unix()}} {
		if _, err := db.Exec(`INSERT INTO ticker_messages(user_id, text, created_at) VALUES (?, ?, ?)`, uid, m.text, m.at); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := a.tickerMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "this week" || msgs[1].Text != "just now" {
		t.Fatalf("tickerMessages = %+v, want only this week's two", msgs)
	}

	a.sweepTicker()
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ticker_messages`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Fatalf("after sweep %d messages remain, want 2", left)
	}
}
