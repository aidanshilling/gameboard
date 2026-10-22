package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Highlights for the top of the boards page: the games most people want to
// play, the games most people own, and the games on your lists that are on
// sale and you don't own yet. The same game on several boards counts once
// (matched by Steam id, then IGDB id, then title).

const highlightsMax = 12

type HighlightUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Highlight struct {
	key        string
	Title      string          `json:"title"`
	Image      string          `json:"image,omitempty"`
	GameID     int64           `json:"gameId"` // the copy to open
	BoardID    int64           `json:"boardId"`
	BoardName  string          `json:"boardName"`
	Boards     int             `json:"boards"` // how many boards it's on
	Users      []HighlightUser `json:"users"`  // who wants it / owns it
	Price      *Price          `json:"price,omitempty"`
	SteamAppID *int64          `json:"steamAppId,omitempty"`
}

// gameKey is what makes two games on different boards the same game.
func gameKey(steam, igdb *int64, title string) string {
	switch {
	case steam != nil:
		return "steam:" + strconv.FormatInt(*steam, 10)
	case igdb != nil:
		return "igdb:" + strconv.FormatInt(*igdb, 10)
	default:
		return "title:" + strings.ToLower(strings.TrimSpace(title))
	}
}

// highlightGame is one copy of a game, as the queries below read it.
type highlightGame struct {
	id, boardID  int64
	title, board string
	steam, igdb  *int64
	image        string
	price        *Price
	key          string
	mine         bool // on a board the user owns or edits
	userID       int64
	username     string
	wants, owns  bool
}

func readMeta(raw string) (image string, price *Price) {
	var m struct {
		HeaderURL string `json:"headerUrl"`
		CoverURL  string `json:"coverUrl"`
		Price     *Price `json:"price"`
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return "", nil
	}
	image = m.HeaderURL
	if image == "" {
		image = m.CoverURL
	}
	return image, m.Price
}

// group collects games by key, keeping each key's copies and people.
type group struct {
	h       Highlight
	users   map[int64]bool
	boards  map[int64]bool
	best    int // people on the copy we'd open, to pick the busiest copy
	perCopy map[int64]int
}

func (a *App) handleHighlights(w http.ResponseWriter, r *http.Request, u *User) {
	// Everyone's "want to play" and "own it" marks, with their games.
	rows, err := a.db.Query(`SELECT g.id, g.title, g.board_id, b.name, g.steam_appid, g.igdb_id, g.meta,
		i.user_id, us.username, i.level = 2, i.owns
		FROM interests i JOIN games g ON g.id = i.game_id JOIN boards b ON b.id = g.board_id
		JOIN users us ON us.id = i.user_id
		WHERE i.level = 2 OR i.owns = 1`)
	if err != nil {
		serverErr(w, err)
		return
	}
	var marks []highlightGame
	for rows.Next() {
		var g highlightGame
		var meta string
		if err := rows.Scan(&g.id, &g.title, &g.boardID, &g.board, &g.steam, &g.igdb, &meta,
			&g.userID, &g.username, &g.wants, &g.owns); err != nil {
			rows.Close()
			serverErr(w, err)
			return
		}
		g.image, g.price = readMeta(meta)
		g.key = gameKey(g.steam, g.igdb, g.title)
		marks = append(marks, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		serverErr(w, err)
		return
	}

	wanted := rank(marks, func(g highlightGame) bool { return g.wants }, highlightsMax)
	owned := rank(marks, func(g highlightGame) bool { return g.owns }, highlightsMax)

	// What the user already owns (any copy counts), and who wants what.
	iOwn := map[string]bool{}
	wanters := map[string][]HighlightUser{}
	for _, g := range rank(marks, func(g highlightGame) bool { return g.wants }, 0) {
		wanters[g.key] = g.Users
	}
	for _, g := range marks {
		if g.userID == u.ID && g.owns {
			iOwn[g.key] = true
		}
	}
	sales, err := a.salesFor(u, iOwn, wanters)
	if err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wanted": wanted, "owned": owned, "sales": sales})
}

// rank counts, for each game, the people whose marks pass keep, and returns
// up to max games (0: all), most people first.
func rank(marks []highlightGame, keep func(highlightGame) bool, max int) []Highlight {
	groups := map[string]*group{}
	for _, g := range marks {
		if !keep(g) {
			continue
		}
		gr := groups[g.key]
		if gr == nil {
			gr = &group{users: map[int64]bool{}, boards: map[int64]bool{}, perCopy: map[int64]int{}}
			gr.h.key = g.key
			groups[g.key] = gr
		}
		gr.boards[g.boardID] = true
		gr.perCopy[g.id]++
		// Open the copy the most people marked.
		if n := gr.perCopy[g.id]; n > gr.best {
			gr.best = n
			gr.h.Title, gr.h.GameID, gr.h.BoardID, gr.h.BoardName = g.title, g.id, g.boardID, g.board
			gr.h.SteamAppID = g.steam
		}
		if gr.h.Image == "" {
			gr.h.Image = g.image
		}
		if gr.h.Price == nil {
			gr.h.Price = g.price
		}
		if !gr.users[g.userID] {
			gr.users[g.userID] = true
			gr.h.Users = append(gr.h.Users, HighlightUser{g.userID, g.username})
		}
	}
	list := make([]Highlight, 0, len(groups))
	for _, gr := range groups {
		gr.h.Boards = len(gr.boards)
		sort.Slice(gr.h.Users, func(i, j int) bool {
			return strings.ToLower(gr.h.Users[i].Username) < strings.ToLower(gr.h.Users[j].Username)
		})
		list = append(list, gr.h)
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i].Users) != len(list[j].Users) {
			return len(list[i].Users) > len(list[j].Users)
		}
		return strings.ToLower(list[i].Title) < strings.ToLower(list[j].Title)
	})
	if max > 0 && len(list) > max {
		list = list[:max]
	}
	return list
}

// salesFor lists games on sale that are on the user's lists (boards they own
// or edit, or games they've said they want to play or might), that they
// don't own. Biggest discounts first; each shows who wants it.
func (a *App) salesFor(u *User, iOwn map[string]bool, wanters map[string][]HighlightUser) ([]Highlight, error) {
	rows, err := a.db.Query(`SELECT g.id, g.title, g.board_id, b.name, g.steam_appid, g.igdb_id, g.meta,
		(b.created_by = ?1 OR EXISTS (SELECT 1 FROM board_members m WHERE m.board_id = b.id AND m.user_id = ?1))
		FROM games g JOIN boards b ON b.id = g.board_id
		WHERE b.created_by = ?1
			OR EXISTS (SELECT 1 FROM board_members m WHERE m.board_id = b.id AND m.user_id = ?1)
			OR EXISTS (SELECT 1 FROM interests i WHERE i.game_id = g.id AND i.user_id = ?1 AND i.level > 0)`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]*Highlight{}
	for rows.Next() {
		var g highlightGame
		var meta string
		if err := rows.Scan(&g.id, &g.title, &g.boardID, &g.board, &g.steam, &g.igdb, &meta, &g.mine); err != nil {
			return nil, err
		}
		g.image, g.price = readMeta(meta)
		g.key = gameKey(g.steam, g.igdb, g.title)
		if iOwn[g.key] || g.price == nil || g.price.Free || g.price.DiscountPercent <= 0 {
			continue
		}
		h := byKey[g.key]
		if h == nil {
			h = &Highlight{Title: g.title, Image: g.image, GameID: g.id, BoardID: g.boardID, BoardName: g.board, Price: g.price, SteamAppID: g.steam}
			byKey[g.key] = h
		} else if g.mine && h.BoardID != g.boardID {
			// Prefer opening the copy on the user's own board.
			h.GameID, h.BoardID, h.BoardName = g.id, g.boardID, g.board
		}
		h.Boards++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]Highlight, 0, len(byKey))
	for key, h := range byKey {
		h.Users = wanters[key]
		if h.Users == nil {
			h.Users = []HighlightUser{}
		}
		list = append(list, *h)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Price.DiscountPercent != list[j].Price.DiscountPercent {
			return list[i].Price.DiscountPercent > list[j].Price.DiscountPercent
		}
		return strings.ToLower(list[i].Title) < strings.ToLower(list[j].Title)
	})
	if len(list) > highlightsMax {
		list = list[:highlightsMax]
	}
	return list, nil
}
