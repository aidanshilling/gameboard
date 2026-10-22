package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// A game "everywhere": every copy of one game across the boards (matched the
// same way as Top Games: Steam id, then IGDB id, then title), seen as one.
// The Games page opens this from Top Games; voting here votes on every copy.

type Copy struct {
	GameID    int64  `json:"gameId"`
	BoardID   int64  `json:"boardId"`
	BoardName string `json:"boardName"`
}

type GameEverywhere struct {
	Title      string          `json:"title"`
	SteamAppID *int64          `json:"steamAppId,omitempty"`
	IGDBID     *int64          `json:"igdbId,omitempty"`
	Meta       json.RawMessage `json:"meta"`
	Copies     []Copy          `json:"copies"`
	Votes      []Interest      `json:"votes"` // one per person, across all copies
	Me         Interest        `json:"me"`
}

// gameCopies finds every copy of the game with this id.
func (a *App) gameCopies(id int64) ([]*Game, error) {
	g, err := a.getGame(id)
	if err != nil || g == nil {
		return nil, err
	}
	switch {
	case g.SteamAppID != nil:
		return a.queryGames(`WHERE g.steam_appid = ?`, *g.SteamAppID)
	case g.IGDBID != nil:
		return a.queryGames(`WHERE g.steam_appid IS NULL AND g.igdb_id = ?`, *g.IGDBID)
	default:
		return a.queryGames(`WHERE g.steam_appid IS NULL AND g.igdb_id IS NULL AND lower(trim(g.title)) = lower(trim(?))`, g.Title)
	}
}

// loadCopies is gameCopies for a handler: it writes the error if there are none.
func (a *App) loadCopies(w http.ResponseWriter, r *http.Request) []*Game {
	id, ok := pathID(w, r)
	if !ok {
		return nil
	}
	copies, err := a.gameCopies(id)
	if err != nil {
		serverErr(w, err)
		return nil
	}
	if len(copies) == 0 {
		writeErr(w, http.StatusNotFound, "game not found")
		return nil
	}
	return copies
}

func (a *App) writeEverywhere(w http.ResponseWriter, u *User, copies []*Game) {
	// The copy with the most metadata speaks for the game.
	best := copies[0]
	for _, c := range copies {
		if len(c.Meta) > len(best.Meta) {
			best = c
		}
	}
	out := GameEverywhere{Title: best.Title, SteamAppID: best.SteamAppID, IGDBID: best.IGDBID, Meta: best.Meta, Copies: []Copy{}, Votes: []Interest{}}
	people := map[int64]*Interest{}
	for _, c := range copies {
		b, err := a.getBoard(c.BoardID)
		if err != nil {
			serverErr(w, err)
			return
		}
		if b != nil {
			out.Copies = append(out.Copies, Copy{c.ID, b.ID, b.Name})
		}
		// Someone's take on the game is their keenest vote on any copy, and
		// they own it if they said so anywhere.
		for _, i := range c.Interests {
			p := people[i.UserID]
			if p == nil {
				v := i
				people[i.UserID] = &v
				continue
			}
			if i.Level > p.Level {
				p.Level = i.Level
			}
			p.Owns = p.Owns || i.Owns
		}
	}
	for _, p := range people {
		out.Votes = append(out.Votes, *p)
		if p.UserID == u.ID {
			out.Me = *p
		}
	}
	sort.Slice(out.Votes, func(i, j int) bool {
		x, y := out.Votes[i], out.Votes[j]
		if x.Level != y.Level {
			return x.Level > y.Level
		}
		return strings.ToLower(x.Username) < strings.ToLower(y.Username)
	})
	sort.Slice(out.Copies, func(i, j int) bool {
		return strings.ToLower(out.Copies[i].BoardName) < strings.ToLower(out.Copies[j].BoardName)
	})
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleGameEverywhere(w http.ResponseWriter, r *http.Request, u *User) {
	if copies := a.loadCopies(w, r); copies != nil {
		a.writeEverywhere(w, u, copies)
	}
}

// handleSetInterestEverywhere sets the user's vote on every copy of a game.
// Anyone can vote on any board's games, so no edit rights are needed.
func (a *App) handleSetInterestEverywhere(w http.ResponseWriter, r *http.Request, u *User) {
	copies := a.loadCopies(w, r)
	if copies == nil {
		return
	}
	var req struct {
		Level int  `json:"level"`
		Owns  bool `json:"owns"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Level < -1 || req.Level > 2 {
		writeErr(w, http.StatusBadRequest, "invalid interest level")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		serverErr(w, err)
		return
	}
	defer tx.Rollback()
	for _, c := range copies {
		if req.Level == 0 && !req.Owns {
			_, err = tx.Exec(`DELETE FROM interests WHERE game_id = ? AND user_id = ?`, c.ID, u.ID)
		} else {
			_, err = tx.Exec(`INSERT INTO interests(game_id, user_id, level, owns) VALUES (?, ?, ?, ?)
				ON CONFLICT(game_id, user_id) DO UPDATE SET level = excluded.level, owns = excluded.owns`,
				c.ID, u.ID, req.Level, req.Owns)
		}
		if err != nil {
			serverErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, err)
		return
	}
	for _, c := range copies {
		a.touchBoard(c.BoardID)
	}
	fresh, err := a.gameCopies(copies[0].ID)
	if err != nil || len(fresh) == 0 {
		serverErr(w, err)
		return
	}
	a.writeEverywhere(w, u, fresh)
}
