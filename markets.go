package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/playwright-community/playwright-go"
)

type Market struct {
	MarketID     string `json:"market_id"`
	URL          string `json:"url"`
	Text         string `json:"text"`
	ScrapedAtUTC string `json:"scraped_at_utc"`
}

type MarketsState struct {
	UpdatedAtUTC string   `json:"updated_at_utc"`
	Markets      []Market `json:"markets"`
}

func gotoFuturePage(page playwright.Page) error {
	if _, err := page.Goto(startURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateCommit,
		Timeout:   playwright.Float(30000),
	}); err != nil {
		return err
	}
	dismissCookieBanner(page)
	page.WaitForTimeout(8000)
	saveDebug(page, "pp_page_state.png")
	return page.Locator(raceLinkSelector).First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(60000),
	})
}

func scrapeMarkets(page playwright.Page) ([]Market, error) {
	scrapedAt := nowUTC()
	links := page.Locator(raceLinkSelector)
	count, err := links.Count()
	if err != nil {
		return nil, err
	}
	logInfo("Found %d future market links", count)

	seen := make(map[string]bool)
	var markets []Market

	for i := 0; i < count; i++ {
		item := links.Nth(i)
		href, err := item.GetAttribute("href")
		if err != nil || href == "" {
			continue
		}
		fullURL := joinURL(baseURL, href)
		if seen[fullURL] {
			continue
		}
		seen[fullURL] = true

		text := ""
		if t, err := item.InnerText(playwright.LocatorInnerTextOptions{Timeout: playwright.Float(3000)}); err == nil {
			text = strings.TrimSpace(t)
		}

		markets = append(markets, Market{
			MarketID:     fullURL,
			URL:          fullURL,
			Text:         text,
			ScrapedAtUTC: scrapedAt,
		})
	}
	return markets, nil
}

func runWatchMarkets() error {
	defer setupLogging(filepath.Join("logs", "watch_markets.log"))()

	var state MarketsState
	loadJSON(marketsStateFile, &state)

	oldIDs := make(map[string]bool)
	for _, m := range state.Markets {
		oldIDs[m.MarketID] = true
	}

	logInfo("Starting playwright")
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("playwright run: %w", err)
	}
	defer pw.Stop()

	logInfo("Launching browser")
	browser, ctx, err := createBrowserContext(pw)
	if err != nil {
		return err
	}
	defer browser.Close()

	logInfo("Opening page")
	page, err := ctx.NewPage()
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}

	markets, err := func() ([]Market, error) {
		if err := gotoFuturePage(page); err != nil {
			saveDebug(page, "watch_markets_timeout.png")
			return nil, fmt.Errorf("goto future page: %w", err)
		}
		return scrapeMarkets(page)
	}()
	if err != nil {
		return err
	}

	var newMarkets []Market
	for _, m := range markets {
		if !oldIDs[m.MarketID] {
			newMarkets = append(newMarkets, m)
		}
	}

	for _, m := range newMarkets {
		text := m.Text
		if text == "" {
			text = "Unnamed market"
		}
		sendTelegram("🏇 New future market found\n" + text + "\n" + m.URL)
		logInfo("NEW MARKET | %s | %s", m.Text, m.URL)
	}

	saveJSON(marketsStateFile, MarketsState{
		UpdatedAtUTC: nowUTC(),
		Markets:      markets,
	})
	logInfo("Saved %d markets, %d new", len(markets), len(newMarkets))
	return nil
}
