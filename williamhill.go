package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/playwright-community/playwright-go"
)

const (
	whStartURL = "https://sports.williamhill.com/betting/en-gb/horse-racing/future-races"
	whBaseURL  = "https://sports.williamhill.com"

	whMarketsStateFile = "state/wh_markets_state.json"
	whRunnersStateFile = "state/wh_runners_state.json"

	// Scoped to the main app to avoid clicking betslip/my-bets toggles
	whSectionToggleSelector = `[data-test-id="ngs-desktop-horse-racing-future-races-app"] button[aria-expanded="false"]`
	whMarketLinkSelector    = `a[href*="/future-races/OB_TY"]`
	whRunnerSelector        = `[data-testid*="selection"]`
)

func whDismissCookieBanner(page playwright.Page) {
	for _, sel := range []string{
		"#cookieBannerAccept",
		`button:has-text("Accept all cookies")`,
	} {
		btn := page.Locator(sel).First()
		if visible, err := btn.IsVisible(); err == nil && visible {
			if err := btn.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}); err == nil {
				logInfo("[WH] Dismissed cookie banner")
				page.WaitForTimeout(1000)
				return
			}
		}
	}
}

func whGotoFuturesPage(page playwright.Page) error {
	if _, err := page.Goto(whStartURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		return err
	}
	whDismissCookieBanner(page)
	page.WaitForTimeout(4000)

	return page.Locator(`[data-test-id="ngs-desktop-horse-racing-future-races-app"]`).WaitFor(
		playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(20000)},
	)
}

func whExpandAllSections(page playwright.Page) {
	toggles := page.Locator(whSectionToggleSelector)
	count, _ := toggles.Count()
	logInfo("[WH] Expanding %d collapsed sections", count)
	for i := 0; i < count; i++ {
		_ = toggles.First().Click(playwright.LocatorClickOptions{Timeout: playwright.Float(3000)})
		page.WaitForTimeout(400)
	}
	page.WaitForTimeout(1500)
}

func whScrapeMarkets(page playwright.Page) ([]Market, error) {
	scrapedAt := nowUTC()
	links := page.Locator(whMarketLinkSelector)
	count, err := links.Count()
	if err != nil {
		return nil, err
	}
	logInfo("[WH] Found %d market links", count)

	seen := make(map[string]bool)
	var markets []Market

	for i := 0; i < count; i++ {
		item := links.Nth(i)
		href, err := item.GetAttribute("href")
		if err != nil || href == "" {
			continue
		}
		fullURL := href
		if !strings.HasPrefix(href, "http") {
			fullURL = whBaseURL + href
		}
		if seen[fullURL] {
			continue
		}
		seen[fullURL] = true

		text := ""
		if t, err := item.InnerText(); err == nil {
			text = strings.TrimSpace(strings.ReplaceAll(t, "\n", " - "))
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

func whExtractRunners(page playwright.Page) ([]Runner, error) {
	rows := page.Locator(whRunnerSelector)
	count, err := rows.Count()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var runners []Runner

	for i := 0; i < count; i++ {
		text, err := rows.Nth(i).InnerText()
		if err != nil {
			logWarn("[WH] Failed to read runner row %d: %s", i, err)
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(text), "\n", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		odds := ""
		if len(parts) > 1 {
			odds = strings.TrimSpace(parts[1])
		}

		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		runners = append(runners, Runner{Name: name, Odds: odds})
	}
	return runners, nil
}

func whCheckOneMarket(page playwright.Page, market Market, state *RunnersState) error {
	marketText := market.Text
	if marketText == "" {
		marketText = "Unnamed market"
	}

	logInfo("[WH] Checking runners for market: %s", market.URL)

	if _, err := page.Goto(market.URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		return err
	}
	whDismissCookieBanner(page)
	page.WaitForTimeout(2500)

	if err := page.Locator(whRunnerSelector).First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(25000),
	}); err != nil {
		return fmt.Errorf("runners not visible: %w", err)
	}

	currentRunners, err := whExtractRunners(page)
	if err != nil {
		return err
	}
	logInfo("[WH] Found %d runners", len(currentRunners))

	known := state.Markets[market.MarketID]
	if known.Runners == nil {
		known.Runners = make(map[string]Runner)
	}

	var updates []RunnerUpdate
	for _, r := range currentRunners {
		key := strings.ToLower(strings.TrimSpace(r.Name))
		existing, found := known.Runners[key]
		if !found {
			logInfo("[WH] NEW RUNNER | %s | %s | %s", marketText, r.Name, r.Odds)
			updates = append(updates, RunnerUpdate{Name: r.Name, IsNew: true, NewOdds: r.Odds})
		} else if existing.Odds != r.Odds {
			logInfo("[WH] ODDS UPDATE | %s | %s | %s -> %s", marketText, r.Name, existing.Odds, r.Odds)
			updates = append(updates, RunnerUpdate{Name: r.Name, OldOdds: existing.Odds, NewOdds: r.Odds})
		}
	}

	if len(updates) > 0 {
		sendMarketUpdate("William Hill", marketText, market.URL, updates)
	}

	runners := make(map[string]Runner, len(currentRunners))
	for _, r := range currentRunners {
		key := strings.ToLower(strings.TrimSpace(r.Name))
		runners[key] = r
	}
	state.Markets[market.MarketID] = MarketRunners{
		MarketText:   marketText,
		MarketURL:    market.URL,
		CheckedAtUTC: nowUTC(),
		Runners:      runners,
	}
	return nil
}

func runWHMonitor() error {
	defer setupLogging(filepath.Join("logs", "wh_monitor.log"))()

	var marketsState MarketsState
	loadJSON(whMarketsStateFile, &marketsState)

	var runnersState RunnersState
	runnersState.Markets = make(map[string]MarketRunners)
	loadJSON(whRunnersStateFile, &runnersState)
	if runnersState.Markets == nil {
		runnersState.Markets = make(map[string]MarketRunners)
	}

	oldIDs := make(map[string]bool)
	for _, m := range marketsState.Markets {
		oldIDs[m.MarketID] = true
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("playwright run: %w", err)
	}
	defer pw.Stop()

	browser, ctx, err := createBrowserContext(pw)
	if err != nil {
		return err
	}
	defer browser.Close()

	page, err := ctx.NewPage()
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}

	logInfo("[WH] Loading futures page")
	if err := whGotoFuturesPage(page); err != nil {
		saveDebug(page, "wh_monitor_load_fail.png")
		return fmt.Errorf("goto futures page: %w", err)
	}

	whExpandAllSections(page)

	currentMarkets, err := whScrapeMarkets(page)
	if err != nil {
		saveDebug(page, "wh_monitor_scrape_fail.png")
		return fmt.Errorf("scrape markets: %w", err)
	}

	var newMarkets []Market
	for _, m := range currentMarkets {
		if !oldIDs[m.MarketID] {
			newMarkets = append(newMarkets, m)
		}
	}
	for _, m := range newMarkets {
		text := m.Text
		if text == "" {
			text = "Unnamed market"
		}
		sendTelegram("🏇 New WH future market found\n" + text + "\n" + m.URL)
		logInfo("[WH] NEW MARKET | %s", m.Text)
	}

	saveJSON(whMarketsStateFile, MarketsState{
		UpdatedAtUTC: nowUTC(),
		Markets:      currentMarkets,
	})
	logInfo("[WH] Saved %d markets, %d new", len(currentMarkets), len(newMarkets))
	logInfo("[WH] Checking all %d markets sequentially", len(currentMarkets))

	for idx, market := range currentMarkets {
		logInfo("[WH] Market %d/%d", idx+1, len(currentMarkets))
		if err := whCheckOneMarket(page, market, &runnersState); err != nil {
			logWarn("[WH] Error on market %s: %s", market.URL, err)
			saveDebug(page, fmt.Sprintf("wh_runner_error_%d.png", idx+1))

			// If the browser itself crashed, recreate the full browser context.
			// A simple page.Close+NewPage won't help if the underlying browser is dead.
			isCrash := strings.Contains(err.Error(), "crashed") || strings.Contains(err.Error(), "Target closed")
			_ = page.Close()
			if isCrash {
				logWarn("[WH] Browser crash detected — recreating browser context")
				_ = browser.Close()
				browser, ctx, err = createBrowserContext(pw)
				if err != nil {
					logWarn("[WH] Failed to recreate browser: %s", err)
					break
				}
			}
			page, err = ctx.NewPage()
			if err != nil {
				logWarn("[WH] Failed to recreate page: %s", err)
				break
			}
			page.WaitForTimeout(2000)
		} else {
			page.WaitForTimeout(1000)
		}
	}

	runnersState.UpdatedAtUTC = nowUTC()
	saveJSON(whRunnersStateFile, runnersState)
	logInfo("[WH] Monitor run complete")
	return nil
}

func runWHExplore() error {
	defer setupLogging(filepath.Join("logs", "wh_explore.log"))()

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("playwright run: %w", err)
	}
	defer pw.Stop()

	browser, ctx, err := createBrowserContext(pw)
	if err != nil {
		return err
	}
	defer browser.Close()

	page, err := ctx.NewPage()
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}

	logInfo("[WH] Navigating to futures page")
	if err := whGotoFuturesPage(page); err != nil {
		saveDebug(page, "wh_explore_load_fail.png")
		return err
	}
	saveDebug(page, "wh_explore_01_loaded.png")

	whExpandAllSections(page)
	saveDebug(page, "wh_explore_02_expanded.png")

	// Check for any remaining collapsed elements anywhere on the page
	anyCollapsed := page.Locator(`[aria-expanded="false"]`)
	count, _ := anyCollapsed.Count()
	logInfo("[WH] Remaining collapsed elements after expand: %d", count)
	for i := 0; i < count && i < 20; i++ {
		el := anyCollapsed.Nth(i)
		tag, _ := el.Evaluate("el => el.tagName", nil)
		text, _ := el.InnerText()
		text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
		if len(text) > 80 {
			text = text[:80]
		}
		logInfo("  [%d] tag=%v text=%q", i, tag, text)
	}

	// Check for "show more" / "view all" type buttons
	for _, sel := range []string{
		`button:has-text("Show more")`,
		`button:has-text("View all")`,
		`button:has-text("See all")`,
		`button:has-text("More")`,
		`[data-testid*="show-more"]`,
		`[data-testid*="view-all"]`,
		`[data-testid*="load-more"]`,
	} {
		n, _ := page.Locator(sel).Count()
		if n > 0 {
			logInfo("[WH] Found %d elements matching %q", n, sel)
			for i := 0; i < n && i < 5; i++ {
				t, _ := page.Locator(sel).Nth(i).InnerText()
				logInfo("  [%d] %q", i, strings.TrimSpace(t))
			}
		}
	}

	// Dump full page HTML so we can inspect section structure
	html, _ := page.Evaluate(`() => document.querySelector('[data-test-id="ngs-desktop-horse-racing-future-races-app"]')?.innerHTML`, nil)
	htmlStr := fmt.Sprintf("%v", html)
	// Write to a file so it's readable
	_ = os.WriteFile("debug/wh_explore_page.html", []byte(htmlStr), 0644)
	logInfo("[WH] Wrote full app HTML to debug/wh_explore_page.html (%d bytes)", len(htmlStr))

	markets, _ := whScrapeMarkets(page)
	logInfo("[WH] Found %d markets after expansion:", len(markets))
	for _, m := range markets {
		logInfo("  %s", m.Text)
	}

	logInfo("[WH] Explore complete")
	return nil
}
