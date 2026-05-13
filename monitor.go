package main

import (
	"fmt"
	"path/filepath"

	"github.com/playwright-community/playwright-go"
)

func runMonitor() error {
	defer setupLogging(filepath.Join("logs", "monitor_paddypower.log"))()

	var marketsState MarketsState
	loadJSON(marketsStateFile, &marketsState)

	var runnersState RunnersState
	runnersState.Markets = make(map[string]MarketRunners)
	loadJSON(runnersStateFile, &runnersState)
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

	logInfo("Loading future markets page")
	currentMarkets, err := func() ([]Market, error) {
		if err := gotoFuturePage(page); err != nil {
			saveDebug(page, "monitor_markets_timeout.png")
			return nil, fmt.Errorf("goto future page: %w", err)
		}
		return scrapeMarkets(page)
	}()
	if err != nil {
		return err
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
		sendTelegram("🏇 New future market found\n" + text + "\n" + m.URL)
		logInfo("NEW MARKET | %s | %s", m.Text, m.URL)
	}

	saveJSON(marketsStateFile, MarketsState{
		UpdatedAtUTC: nowUTC(),
		Markets:      currentMarkets,
	})
	logInfo("Saved %d markets, %d new", len(currentMarkets), len(newMarkets))
	logInfo("Checking all %d markets sequentially", len(currentMarkets))

	for idx, market := range currentMarkets {
		logInfo("Market %d/%d", idx+1, len(currentMarkets))
		if err := checkOneMarket(page, market, &runnersState); err != nil {
			logWarn("Error on market %s: %s", market.URL, err)
			saveDebug(page, fmt.Sprintf("monitor_runner_error_%d.png", idx+1))
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
	logInfo("Full monitor run complete")
	return nil
}
