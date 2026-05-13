package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/playwright-community/playwright-go"
)

type Runner struct {
	Name string `json:"name"`
	Odds string `json:"odds"`
}

type MarketRunners struct {
	MarketText   string             `json:"market_text"`
	MarketURL    string             `json:"market_url"`
	CheckedAtUTC string             `json:"checked_at_utc"`
	Runners      map[string]Runner  `json:"runners"`
}

type RunnersState struct {
	UpdatedAtUTC string                    `json:"updated_at_utc"`
	Markets      map[string]MarketRunners  `json:"markets"`
}

func extractRunners(page playwright.Page) ([]Runner, error) {
	rows := page.Locator(runnerRowSelector)
	count, err := rows.Count()
	if err != nil {
		return nil, err
	}

	var runners []Runner
	for i := 0; i < count; i++ {
		row := rows.Nth(i)
		name, err := row.Locator(runnerNameSelector).First().InnerText(
			playwright.LocatorInnerTextOptions{Timeout: playwright.Float(2000)},
		)
		if err != nil {
			logWarn("Failed to parse runner row %d: %s", i, err)
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		odds := ""
		if o, err := row.Locator(runnerOddsSelector).First().InnerText(
			playwright.LocatorInnerTextOptions{Timeout: playwright.Float(1500)},
		); err == nil {
			odds = strings.TrimSpace(o)
		}

		runners = append(runners, Runner{Name: name, Odds: odds})
	}

	seen := make(map[string]bool)
	var deduped []Runner
	for _, r := range runners {
		key := strings.ToLower(strings.TrimSpace(r.Name))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, r)
	}
	return deduped, nil
}

func checkOneMarket(page playwright.Page, market Market, state *RunnersState) error {
	marketText := strings.TrimSpace(market.Text)
	if marketText == "" {
		marketText = "Unnamed market"
	}

	logInfo("Checking runners for market: %s", market.URL)

	if _, err := page.Goto(market.URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		return err
	}
	dismissCookieBanner(page)
	page.WaitForTimeout(2500)

	if err := page.Locator(runnerRowSelector).First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(25000),
	}); err != nil {
		return err
	}

	currentRunners, err := extractRunners(page)
	if err != nil {
		return err
	}
	logInfo("Found %d runners on page", len(currentRunners))

	known := state.Markets[market.MarketID]
	if known.Runners == nil {
		known.Runners = make(map[string]Runner)
	}

	var updates []RunnerUpdate
	for _, r := range currentRunners {
		key := strings.ToLower(strings.TrimSpace(r.Name))
		existing, found := known.Runners[key]
		if !found {
			logInfo("NEW RUNNER | %s | %s | %s", marketText, r.Name, r.Odds)
			updates = append(updates, RunnerUpdate{Name: r.Name, IsNew: true, NewOdds: r.Odds})
		} else if existing.Odds != r.Odds {
			logInfo("ODDS UPDATE | %s | %s | %s -> %s", marketText, r.Name, existing.Odds, r.Odds)
			updates = append(updates, RunnerUpdate{Name: r.Name, OldOdds: existing.Odds, NewOdds: r.Odds})
		}
	}

	if len(updates) > 0 {
		var whState RunnersState
		whState.Markets = make(map[string]MarketRunners)
		loadJSON(whRunnersStateFile, &whState)
		for i := range updates {
			updates[i].CrossOdds = lookupOddsInState(&whState, updates[i].Name)
		}
		sendMarketUpdate("Paddy Power", "William Hill", marketText, market.URL, updates)
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

func runCheckRunners() error {
	defer setupLogging(filepath.Join("logs", "check_runners.log"))()

	var marketsState MarketsState
	loadJSON(marketsStateFile, &marketsState)

	if len(marketsState.Markets) == 0 {
		logInfo("No markets found in markets_state.json, nothing to do")
		return nil
	}

	var runnersState RunnersState
	runnersState.Markets = make(map[string]MarketRunners)
	loadJSON(runnersStateFile, &runnersState)
	if runnersState.Markets == nil {
		runnersState.Markets = make(map[string]MarketRunners)
	}

	logInfo("Checking all %d markets sequentially", len(marketsState.Markets))

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

	for idx, market := range marketsState.Markets {
		logInfo("Market %d/%d", idx+1, len(marketsState.Markets))
		if err := checkOneMarket(page, market, &runnersState); err != nil {
			logWarn("Error on market %s: %s", market.URL, err)
			saveDebug(page, fmt.Sprintf("check_runners_error_%d.png", idx+1))
			_ = page.Close()
			page, err = ctx.NewPage()
			if err != nil {
				logWarn("Failed to recreate page: %s", err)
				break
			}
			page.WaitForTimeout(2000)
		} else {
			page.WaitForTimeout(1000)
		}
	}

	runnersState.UpdatedAtUTC = nowUTC()
	saveJSON(runnersStateFile, runnersState)
	logInfo("Runner check complete")
	return nil
}
