package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Steam prices change with sales, but a game's metadata is only fetched when
// it's added or someone refreshes it. So every half hour this checks the
// prices that are oldest (over six hours old), up to 100 games in a single
// request to Steam, and updates just the price in each game's metadata.

const (
	priceCheckEvery = 30 * time.Minute
	priceMaxAge     = 6 * time.Hour
	priceBatch      = 100 // Steam takes many app ids at once when only asked for prices
)

func (a *App) checkPricesLoop(ctx context.Context) {
	// Give the server a minute to settle after starting.
	wait := time.NewTimer(time.Minute)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		}
		if err := a.checkPrices(ctx); err != nil {
			log.Printf("price check: %v", err)
		}
		wait.Reset(priceCheckEvery)
	}
}

// checkPrices refreshes the stalest batch of Steam prices.
func (a *App) checkPrices(ctx context.Context) error {
	rows, err := a.db.Query(`SELECT steam_appid FROM games WHERE steam_appid IS NOT NULL
		GROUP BY steam_appid HAVING MIN(price_checked_at) < ? ORDER BY MIN(price_checked_at) LIMIT ?`,
		now()-int64(priceMaxAge.Seconds()), priceBatch)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}

	prices, err := a.meta.steamPrices(ctx, ids)
	if err != nil {
		return err
	}
	changed := 0
	for _, id := range ids {
		n, err := a.updatePrice(id, prices[id])
		if err != nil {
			return err
		}
		changed += n
	}
	if changed > 0 {
		log.Printf("price check: %d of %d games' prices changed", changed, len(ids))
	}
	return nil
}

// updatePrice stores a fresh price on every game with that Steam app id,
// returning how many changed. With no price from Steam (free, not out yet,
// or no answer), a game keeps what it had but can't stay "on sale".
func (a *App) updatePrice(appID string, p *Price) (int, error) {
	rows, err := a.db.Query(`SELECT id, meta FROM games WHERE steam_appid = ?`, appID)
	if err != nil {
		return 0, err
	}
	type row struct {
		id   int64
		meta string
	}
	var games []row
	for rows.Next() {
		var g row
		if err := rows.Scan(&g.id, &g.meta); err != nil {
			rows.Close()
			return 0, err
		}
		games = append(games, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	for _, g := range games {
		var m Meta
		if err := json.Unmarshal([]byte(g.meta), &m); err != nil {
			continue // leave metadata we can't read alone
		}
		old, _ := json.Marshal(m.Price)
		switch {
		case p != nil:
			m.Price = p
		case m.Price != nil && m.Price.DiscountPercent > 0:
			m.Price.DiscountPercent = 0
			m.Price.Initial = ""
		}
		meta, err := json.Marshal(m)
		if err != nil {
			return changed, err
		}
		fresh, _ := json.Marshal(m.Price)
		if string(old) != string(fresh) {
			changed++
		}
		if _, err := a.db.Exec(`UPDATE games SET meta = ?, price_checked_at = ? WHERE id = ?`, string(meta), now(), g.id); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// steamPrices asks Steam for just the prices of several apps. Apps it
// doesn't sell (free, or not out yet) or didn't answer for are left out.
func (m *metaClient) steamPrices(ctx context.Context, ids []string) (map[string]*Price, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := fmt.Sprintf("https://store.steampowered.com/api/appdetails?filters=price_overview&cc=%s&appids=%s",
		url.QueryEscape(m.country), strings.Join(ids, ","))
	var resp map[string]struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"` // an object, or [] when there's no price
	}
	if err := m.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := map[string]*Price{}
	for id, e := range resp {
		if !e.Success {
			continue
		}
		var d struct {
			PriceOverview *struct {
				Final            int    `json:"final"`
				DiscountPercent  int    `json:"discount_percent"`
				InitialFormatted string `json:"initial_formatted"`
				FinalFormatted   string `json:"final_formatted"`
			} `json:"price_overview"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.PriceOverview == nil {
			continue
		}
		po := d.PriceOverview
		out[id] = &Price{Cents: po.Final, Final: po.FinalFormatted, Initial: po.InitialFormatted, DiscountPercent: po.DiscountPercent}
	}
	return out, nil
}
