package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metadata sources:
//   - Steam store API (no key): search, price, images, co-op/multiplayer categories
//   - ProtonDB summaries API (no key): Linux/Steam Deck compatibility tier
//   - IGDB (needs free Twitch app credentials): summary, cover, genres, game modes, rating
//   - SteamDB has no public API, so it's linked rather than queried.

const userAgent = "gameboard/1.0 (self-hosted game list)"

var errNotFound = errors.New("not found")

type Meta struct {
	Summary     string      `json:"summary,omitempty"`
	CoverURL    string      `json:"coverUrl,omitempty"`  // portrait (IGDB)
	HeaderURL   string      `json:"headerUrl,omitempty"` // landscape (Steam)
	ReleaseDate string      `json:"releaseDate,omitempty"`
	ComingSoon  bool        `json:"comingSoon,omitempty"`
	Developers  []string    `json:"developers,omitempty"`
	Publishers  []string    `json:"publishers,omitempty"`
	Genres      []string    `json:"genres,omitempty"`
	Modes       []string    `json:"modes,omitempty"`
	Platforms   []string    `json:"platforms,omitempty"`
	Price       *Price      `json:"price,omitempty"`
	Metacritic  int         `json:"metacritic,omitempty"`
	IGDBRating  int         `json:"igdbRating,omitempty"`
	NativeLinux bool        `json:"nativeLinux,omitempty"`
	Proton      *ProtonInfo `json:"proton,omitempty"`
	Website     string      `json:"website,omitempty"`
	Links       Links       `json:"links"`
}

type Price struct {
	Free            bool   `json:"free,omitempty"`
	Cents           int    `json:"cents"`
	Final           string `json:"final,omitempty"`
	Initial         string `json:"initial,omitempty"`
	DiscountPercent int    `json:"discountPercent,omitempty"`
}

type Links struct {
	Steam    string `json:"steam,omitempty"`
	SteamDB  string `json:"steamdb,omitempty"`
	ProtonDB string `json:"protondb,omitempty"`
	IGDB     string `json:"igdb,omitempty"`
}

type ProtonInfo struct {
	Tier             string  `json:"tier"`
	TrendingTier     string  `json:"trendingTier"`
	BestReportedTier string  `json:"bestReportedTier"`
	Score            float64 `json:"score"`
	Confidence       string  `json:"confidence"`
	Total            int     `json:"total"`
}

type SearchResult struct {
	Title      string   `json:"title"`
	SteamAppID *int64   `json:"steamAppId,omitempty"`
	IGDBID     *int64   `json:"igdbId,omitempty"`
	Image      string   `json:"image,omitempty"`
	Year       int      `json:"year,omitempty"`
	Price      string   `json:"price,omitempty"`
	Platforms  []string `json:"platforms,omitempty"`
}

type FetchResult struct {
	Name       string
	SteamAppID *int64
	IGDBID     *int64
	Meta       Meta
}

type metaClient struct {
	http         *http.Client
	country      string
	twitchID     string
	twitchSecret string
	mu           sync.Mutex
	token        string
	tokenExp     time.Time
}

func newMetaClient(cfg Config) *metaClient {
	return &metaClient{
		http:         &http.Client{Timeout: 12 * time.Second},
		country:      cfg.SteamCountry,
		twitchID:     cfg.TwitchClientID,
		twitchSecret: cfg.TwitchSecret,
	}
}

func (m *metaClient) igdbEnabled() bool { return m.twitchID != "" && m.twitchSecret != "" }

func (m *metaClient) do(req *http.Request, out any) error {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s %s", req.Method, req.URL.Host, resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (m *metaClient) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return m.do(req, out)
}

// ---- Steam ----

func steamLinks(id int64) Links {
	return Links{
		Steam:    fmt.Sprintf("https://store.steampowered.com/app/%d/", id),
		SteamDB:  fmt.Sprintf("https://steamdb.info/app/%d/", id),
		ProtonDB: fmt.Sprintf("https://www.protondb.com/app/%d", id),
	}
}

func (m *metaClient) steamSearch(ctx context.Context, q string) ([]SearchResult, error) {
	u := "https://store.steampowered.com/api/storesearch/?l=english&cc=" + url.QueryEscape(m.country) + "&term=" + url.QueryEscape(q)
	var resp struct {
		Items []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			ID        int64  `json:"id"`
			TinyImage string `json:"tiny_image"`
			Price     *struct {
				Currency string `json:"currency"`
				Final    int    `json:"final"`
			} `json:"price"`
		} `json:"items"`
	}
	if err := m.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, it := range resp.Items {
		if it.Type != "app" {
			continue
		}
		id := it.ID
		r := SearchResult{Title: it.Name, SteamAppID: &id, Image: it.TinyImage}
		if it.Price != nil {
			r.Price = fmt.Sprintf("%.2f %s", float64(it.Price.Final)/100, it.Price.Currency)
		}
		out = append(out, r)
	}
	return out, nil
}

type steamApp struct {
	Name             string   `json:"name"`
	IsFree           bool     `json:"is_free"`
	ShortDescription string   `json:"short_description"`
	HeaderImage      string   `json:"header_image"`
	Developers       []string `json:"developers"`
	Publishers       []string `json:"publishers"`
	Website          string   `json:"website"`
	PriceOverview    *struct {
		Final            int    `json:"final"`
		DiscountPercent  int    `json:"discount_percent"`
		InitialFormatted string `json:"initial_formatted"`
		FinalFormatted   string `json:"final_formatted"`
	} `json:"price_overview"`
	Platforms struct {
		Windows bool `json:"windows"`
		Mac     bool `json:"mac"`
		Linux   bool `json:"linux"`
	} `json:"platforms"`
	Metacritic *struct {
		Score int `json:"score"`
	} `json:"metacritic"`
	Categories []struct {
		Description string `json:"description"`
	} `json:"categories"`
	Genres []struct {
		Description string `json:"description"`
	} `json:"genres"`
	ReleaseDate struct {
		ComingSoon bool   `json:"coming_soon"`
		Date       string `json:"date"`
	} `json:"release_date"`
}

func (m *metaClient) steamApp(ctx context.Context, id int64) (*steamApp, error) {
	u := fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%d&l=english&cc=%s", id, url.QueryEscape(m.country))
	var resp map[string]struct {
		Success bool     `json:"success"`
		Data    steamApp `json:"data"`
	}
	if err := m.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	e, ok := resp[strconv.FormatInt(id, 10)]
	if !ok || !e.Success {
		return nil, errNotFound
	}
	return &e.Data, nil
}

func (m *metaClient) steamResult(ctx context.Context, id int64) (SearchResult, error) {
	app, err := m.steamApp(ctx, id)
	if err != nil {
		return SearchResult{}, err
	}
	r := SearchResult{Title: app.Name, SteamAppID: &id, Image: app.HeaderImage}
	if app.PriceOverview != nil {
		r.Price = app.PriceOverview.FinalFormatted
	} else if app.IsFree {
		r.Price = "Free"
	}
	return r, nil
}

// Steam's category list is mostly noise (achievements, cloud saves...); keep the ones about how you play.
var steamModeRe = regexp.MustCompile(`(?i)player|co-op|pvp|split|remote play together|mmo|cross-platform`)

func (m *metaClient) protonDB(ctx context.Context, id int64) (*ProtonInfo, error) {
	var p ProtonInfo
	if err := m.getJSON(ctx, fmt.Sprintf("https://www.protondb.com/api/v1/reports/summaries/%d.json", id), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ---- IGDB ----

func (m *metaClient) igdbToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Now().Before(m.tokenExp) {
		return m.token, nil
	}
	form := url.Values{
		"client_id":     {m.twitchID},
		"client_secret": {m.twitchSecret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://id.twitch.tv/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := m.do(req, &resp); err != nil {
		return "", fmt.Errorf("twitch token: %w", err)
	}
	m.token = resp.AccessToken
	m.tokenExp = time.Now().Add(time.Duration(resp.ExpiresIn)*time.Second - 5*time.Minute)
	return m.token, nil
}

func (m *metaClient) igdbQuery(ctx context.Context, endpoint, body string, out any) error {
	tok, err := m.igdbToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.igdb.com/v4/"+endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Client-ID", m.twitchID)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "text/plain")
	err = m.do(req, out)
	if err != nil && strings.Contains(err.Error(), "401") {
		m.mu.Lock()
		m.token = ""
		m.mu.Unlock()
	}
	return err
}

// igdbString makes s safe to embed in a double-quoted IGDB query string.
func igdbString(s string) string {
	return strings.NewReplacer(`"`, "", `\`, "").Replace(s)
}

type named struct {
	Name string `json:"name"`
}

type igdbGame struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Summary          string  `json:"summary"`
	URL              string  `json:"url"`
	FirstReleaseDate int64   `json:"first_release_date"`
	TotalRating      float64 `json:"total_rating"`
	Cover            *struct {
		ImageID string `json:"image_id"`
	} `json:"cover"`
	Genres    []named `json:"genres"`
	GameModes []named `json:"game_modes"`
	Platforms []struct {
		Name         string `json:"name"`
		Abbreviation string `json:"abbreviation"`
	} `json:"platforms"`
	InvolvedCompanies []struct {
		Company   named `json:"company"`
		Developer bool  `json:"developer"`
		Publisher bool  `json:"publisher"`
	} `json:"involved_companies"`
	Websites []struct {
		URL string `json:"url"`
	} `json:"websites"`
	ExternalGames []struct {
		UID string `json:"uid"`
		URL string `json:"url"`
	} `json:"external_games"`
}

func (g *igdbGame) platformNames() []string {
	var out []string
	for _, p := range g.Platforms {
		if p.Abbreviation != "" {
			out = append(out, p.Abbreviation)
		} else if p.Name != "" {
			out = append(out, p.Name)
		}
	}
	return out
}

func igdbImage(id, size string) string {
	return "https://images.igdb.com/igdb/image/upload/" + size + "/" + id + ".jpg"
}

const igdbSearchFields = `fields name,first_release_date,cover.image_id,platforms.abbreviation,platforms.name;`

func (m *metaClient) igdbSearchResults(ctx context.Context, body string) ([]SearchResult, error) {
	var games []igdbGame
	if err := m.igdbQuery(ctx, "games", body, &games); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, g := range games {
		id := g.ID
		r := SearchResult{Title: g.Name, IGDBID: &id, Platforms: g.platformNames()}
		if g.Cover != nil && g.Cover.ImageID != "" {
			r.Image = igdbImage(g.Cover.ImageID, "t_cover_small")
		}
		if g.FirstReleaseDate > 0 {
			r.Year = time.Unix(g.FirstReleaseDate, 0).UTC().Year()
		}
		out = append(out, r)
	}
	return out, nil
}

// igdbSearchTypes keeps DLC, packs and seasons out of search results: main
// games, standalone expansions, remakes, remasters, expanded games and ports.
const igdbSearchTypes = "(0,4,8,9,10,11)"

func (m *metaClient) igdbSearch(ctx context.Context, q string) ([]SearchResult, error) {
	return m.igdbSearchResults(ctx, fmt.Sprintf(`search "%s"; %s where version_parent = null & game_type = %s; limit 12;`,
		igdbString(q), igdbSearchFields, igdbSearchTypes))
}

func (m *metaClient) igdbSearchSlug(ctx context.Context, slug string) ([]SearchResult, error) {
	return m.igdbSearchResults(ctx, fmt.Sprintf(`%s where slug = "%s"; limit 1;`, igdbSearchFields, igdbString(slug)))
}

func (m *metaClient) igdbGame(ctx context.Context, id int64) (*igdbGame, error) {
	body := fmt.Sprintf(`fields name,summary,url,first_release_date,total_rating,cover.image_id,genres.name,game_modes.name,
		platforms.name,platforms.abbreviation,involved_companies.company.name,involved_companies.developer,
		involved_companies.publisher,websites.url,external_games.uid,external_games.url; where id = %d;`, id)
	var games []igdbGame
	if err := m.igdbQuery(ctx, "games", body, &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, errNotFound
	}
	return &games[0], nil
}

var steamAppURLRe = regexp.MustCompile(`store\.steampowered\.com/app/(\d+)`)

func steamIDFromURL(u string) int64 {
	if m := steamAppURLRe.FindStringSubmatch(u); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		return id
	}
	return 0
}

func steamIDFromIGDB(g *igdbGame) int64 {
	for _, e := range g.ExternalGames {
		if id := steamIDFromURL(e.URL); id != 0 {
			return id
		}
	}
	for _, w := range g.Websites {
		if id := steamIDFromURL(w.URL); id != 0 {
			return id
		}
	}
	return 0
}

// igdbIDForSteam finds the IGDB game that lists the given Steam app as an external store entry.
func (m *metaClient) igdbIDForSteam(ctx context.Context, appID int64) (int64, error) {
	var rows []struct {
		Game int64  `json:"game"`
		UID  string `json:"uid"`
		URL  string `json:"url"`
	}
	body := fmt.Sprintf(`fields game,uid,url; where uid = "%d"; limit 50;`, appID)
	if err := m.igdbQuery(ctx, "external_games", body, &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if steamIDFromURL(r.URL) == appID && r.Game != 0 {
			return r.Game, nil
		}
	}
	return 0, nil
}

// lookupLink fetches the title and picture for a game linked from outside the
// boards, preferring Steam. Linking from IGDB also picks up its Steam id.
func (m *metaClient) lookupLink(ctx context.Context, steamID, igdbID *int64) (*PostLink, error) {
	if steamID != nil {
		app, err := m.steamApp(ctx, *steamID)
		if err != nil {
			return nil, err
		}
		return &PostLink{Title: app.Name, Image: app.HeaderImage, SteamAppID: steamID}, nil
	}
	if igdbID == nil || !m.igdbEnabled() {
		return nil, errNotFound
	}
	g, err := m.igdbGame(ctx, *igdbID)
	if err != nil {
		return nil, err
	}
	link := &PostLink{Title: g.Name, IGDBID: igdbID, IGDBURL: g.URL}
	if g.Cover != nil && g.Cover.ImageID != "" {
		link.Image = igdbImage(g.Cover.ImageID, "t_screenshot_med")
	}
	if sid := steamIDFromIGDB(g); sid != 0 {
		link.SteamAppID = &sid
	}
	return link, nil
}

// ---- combined fetch ----

// Fetch gathers metadata from every source it can, cross-linking Steam and
// IGDB ids where possible. Source failures are logged and skipped.
func (m *metaClient) Fetch(ctx context.Context, steamID, igdbID *int64) FetchResult {
	res := FetchResult{SteamAppID: steamID, IGDBID: igdbID}

	var ig *igdbGame
	if m.igdbEnabled() {
		if res.IGDBID == nil && res.SteamAppID != nil {
			id, err := m.igdbIDForSteam(ctx, *res.SteamAppID)
			if err != nil {
				log.Printf("igdb lookup for steam %d: %v", *res.SteamAppID, err)
			} else if id != 0 {
				res.IGDBID = &id
			}
		}
		if res.IGDBID != nil {
			g, err := m.igdbGame(ctx, *res.IGDBID)
			if err != nil {
				log.Printf("igdb game %d: %v", *res.IGDBID, err)
			} else {
				ig = g
				if res.SteamAppID == nil {
					if sid := steamIDFromIGDB(g); sid != 0 {
						res.SteamAppID = &sid
					}
				}
			}
		}
	}

	var st *steamApp
	var proton *ProtonInfo
	if res.SteamAppID != nil {
		id := *res.SteamAppID
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			var err error
			if st, err = m.steamApp(ctx, id); err != nil {
				log.Printf("steam app %d: %v", id, err)
			}
		}()
		go func() {
			defer wg.Done()
			var err error
			if proton, err = m.protonDB(ctx, id); err != nil && !errors.Is(err, errNotFound) {
				log.Printf("protondb %d: %v", id, err)
			}
		}()
		wg.Wait()
	}

	meta := &res.Meta
	if res.SteamAppID != nil {
		meta.Links = steamLinks(*res.SteamAppID)
	}
	meta.Proton = proton

	if ig != nil {
		res.Name = ig.Name
		meta.Summary = ig.Summary
		meta.Links.IGDB = ig.URL
		if ig.Cover != nil && ig.Cover.ImageID != "" {
			meta.CoverURL = igdbImage(ig.Cover.ImageID, "t_cover_big")
		}
		if ig.FirstReleaseDate > 0 {
			meta.ReleaseDate = time.Unix(ig.FirstReleaseDate, 0).UTC().Format("Jan 2, 2006")
		}
		if ig.TotalRating > 0 {
			meta.IGDBRating = int(ig.TotalRating + 0.5)
		}
		for _, c := range ig.InvolvedCompanies {
			if c.Developer {
				meta.Developers = appendUnique(meta.Developers, c.Company.Name)
			}
			if c.Publisher {
				meta.Publishers = appendUnique(meta.Publishers, c.Company.Name)
			}
		}
		for _, g := range ig.Genres {
			meta.Genres = appendUnique(meta.Genres, g.Name)
		}
		for _, g := range ig.GameModes {
			meta.Modes = appendUnique(meta.Modes, g.Name)
		}
		meta.Platforms = ig.platformNames()
	}

	if st != nil {
		if res.Name == "" {
			res.Name = st.Name
		}
		if meta.Summary == "" {
			meta.Summary = html.UnescapeString(st.ShortDescription)
		}
		meta.HeaderURL = st.HeaderImage
		if st.ReleaseDate.Date != "" {
			meta.ReleaseDate = st.ReleaseDate.Date
		}
		meta.ComingSoon = st.ReleaseDate.ComingSoon
		if len(st.Developers) > 0 {
			meta.Developers = st.Developers
		}
		if len(st.Publishers) > 0 {
			meta.Publishers = st.Publishers
		}
		for _, g := range st.Genres {
			meta.Genres = appendUnique(meta.Genres, g.Description)
		}
		for _, c := range st.Categories {
			if steamModeRe.MatchString(c.Description) {
				meta.Modes = appendUnique(meta.Modes, c.Description)
			}
		}
		if len(meta.Platforms) == 0 {
			if st.Platforms.Windows {
				meta.Platforms = append(meta.Platforms, "Windows")
			}
			if st.Platforms.Mac {
				meta.Platforms = append(meta.Platforms, "Mac")
			}
			if st.Platforms.Linux {
				meta.Platforms = append(meta.Platforms, "Linux")
			}
		}
		meta.NativeLinux = st.Platforms.Linux
		if st.Metacritic != nil {
			meta.Metacritic = st.Metacritic.Score
		}
		switch {
		case st.PriceOverview != nil:
			meta.Price = &Price{
				Cents:           st.PriceOverview.Final,
				Final:           st.PriceOverview.FinalFormatted,
				Initial:         st.PriceOverview.InitialFormatted,
				DiscountPercent: st.PriceOverview.DiscountPercent,
			}
		case st.IsFree:
			meta.Price = &Price{Free: true}
		}
		if strings.HasPrefix(st.Website, "https://") || strings.HasPrefix(st.Website, "http://") {
			meta.Website = st.Website
		}
	}

	if len(meta.Genres) > 8 {
		meta.Genres = meta.Genres[:8]
	}
	return res
}

func appendUnique(list []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return list
	}
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return list
		}
	}
	return append(list, s)
}
