package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

const (
	baseURL  = "https://www.paddypower.com"
	startURL = "https://www.paddypower.com/horse-racing?tab=future"

	marketsStateFile = "state/markets_state.json"
	runnersStateFile = "state/runners_state.json"

	raceLinkSelector   = `a[href^="/horse-racing/future/"]`
	runnerRowSelector  = "racing-runner"
	runnerNameSelector = ".racing-runner__selection-name"
	runnerOddsSelector = ".btn-odds__label"
)

var cookieButtonCandidates = []string{
	"#onetrust-accept-btn-handler",
	`button[aria-label="Accept All"]`,
	`button:has-text("Accept All")`,
	`button:has-text("Allow All")`,
	`button:has-text("Accept")`,
	`button:has-text("I Accept")`,
}

var logger *log.Logger

func ensureDirs() {
	for _, d := range []string{"state", "logs", "debug"} {
		_ = os.MkdirAll(d, 0755)
	}
}

func setupLogging(logFile string) func() {
	ensureDirs()
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	w := io.MultiWriter(f, os.Stdout)
	logger = log.New(w, "", 0)
	return func() { _ = f.Close() }
}

func logf(level, format string, args ...any) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	logger.Printf("%s | %s | %s", ts, level, fmt.Sprintf(format, args...))
}

func logInfo(format string, args ...any)  { logf("INFO", format, args...) }
func logWarn(format string, args ...any)  { logf("WARNING", format, args...) }

func nowUTC() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func loadJSON(path string, v any) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("Failed to load JSON from %s: %s", path, err)
		}
		return
	}
	if err := json.Unmarshal(data, v); err != nil {
		logWarn("Failed to parse JSON from %s: %s", path, err)
	}
}

func saveJSON(path string, v any) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		logWarn("Failed to create dir for %s: %s", path, err)
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		logWarn("Failed to marshal JSON: %s", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		logWarn("Failed to write JSON to %s: %s", path, err)
	}
}

func joinURL(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return base + href
	}
	r, err := url.Parse(href)
	if err != nil {
		return base + href
	}
	return b.ResolveReference(r).String()
}

func createBrowserContext(pw *playwright.Playwright) (playwright.Browser, playwright.BrowserContext, error) {
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless:       playwright.Bool(true),
		ExecutablePath: playwright.String("/home/ubuntu/.cache/ms-playwright/chromium-1200/chrome-linux64/chrome"),
		Args:           []string{"--no-sandbox", "--disable-dev-shm-usage"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("launch browser: %w", err)
	}

	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		Viewport:   &playwright.Size{Width: 1440, Height: 1400},
		Locale:     playwright.String("en-GB"),
		TimezoneId: playwright.String("Europe/London"),
		UserAgent: playwright.String(
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
		),
	})
	if err != nil {
		_ = browser.Close()
		return nil, nil, fmt.Errorf("new context: %w", err)
	}
	return browser, ctx, nil
}

func dismissCookieBanner(page playwright.Page) {
	for _, sel := range cookieButtonCandidates {
		btn := page.Locator(sel).First()
		visible, err := btn.IsVisible()
		if err == nil && visible {
			if err := btn.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}); err == nil {
				logInfo("Dismissed cookie banner with selector: %s", sel)
				page.WaitForTimeout(1000)
				return
			}
		}
	}
	if _, err := page.Evaluate(`() => {
		const el = document.getElementById('onetrust-consent-sdk');
		if (el) el.remove();
	}`); err == nil {
		logInfo("Removed cookie overlay as fallback")
		page.WaitForTimeout(500)
	}
}

func saveDebug(page playwright.Page, name string) {
	path := filepath.Join("debug", name)
	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(true),
	}); err != nil {
		logWarn("Failed screenshot %s: %s", name, err)
		return
	}
	logInfo("Saved screenshot: %s", path)
}

func sendTelegram(text string) bool {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if token == "" || chatID == "" {
		logInfo("Telegram env vars not set, skipping Telegram send")
		return false
	}

	body, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Post(
		"https://api.telegram.org/bot"+token+"/sendMessage",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		logWarn("Telegram exception: %s", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 == 2 {
		logInfo("Telegram message sent")
		return true
	}
	logWarn("Telegram send failed: %d", resp.StatusCode)
	return false
}

// --- Cross-bookie notification helpers ---

type RunnerUpdate struct {
	Name    string
	IsNew   bool
	OldOdds string
	NewOdds string
}

var raceStopWords = map[string]bool{
	"the": true, "and": true, "a": true, "an": true, "of": true,
	"at": true, "in": true, "on": true, "by": true, "for": true,
	"to": true, "la": true, "de": true, "du": true, "les": true,
	"l": true, "rule": true, "apply": true,
}

var raceDateWords = map[string]bool{
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "oct": true, "nov": true,
	"dec": true, "january": true, "february": true, "march": true,
	"april": true, "may": true, "june": true, "july": true, "august": true,
	"september": true, "october": true, "november": true, "december": true,
	"mon": true, "tue": true, "wed": true, "thu": true, "fri": true,
	"sat": true, "sun": true,
}

// normalizeRaceText strips noise (sponsors, dates, suffixes) before comparison.
func normalizeRaceText(s string) string {
	s = strings.ToLower(s)
	for _, p := range []string{
		"(antepost)", "(ante post)", "(ante-post)", "antepost",
		"nrnb", "nrmb", "rule 4 may apply", "rule 4",
		// sponsor prefixes
		"betfred ", "coral ", "boyle sports ", "coolmore ", "trade nation ",
		"qatar ", "boodles ", "singer ", "unibet ", "betway ", "sky bet ",
	} {
		s = strings.ReplaceAll(s, p, "")
	}
	// normalise apostrophe contractions: "l'arc" → "arc", "novices'" → "novices"
	s = strings.ReplaceAll(s, "l'arc", "arc")
	s = strings.ReplaceAll(s, "larc", "arc")
	s = strings.ReplaceAll(s, "'", "")
	return strings.TrimSpace(s)
}

func isNumericOrOrdinal(w string) bool {
	if _, err := strconv.Atoi(w); err == nil {
		return true
	}
	if len(w) >= 3 {
		sfx := w[len(w)-2:]
		if sfx == "st" || sfx == "nd" || sfx == "rd" || sfx == "th" {
			if _, err := strconv.Atoi(w[:len(w)-2]); err == nil {
				return true
			}
		}
	}
	return false
}

func significantRaceWords(marketText string) map[string]bool {
	result := make(map[string]bool)
	for _, w := range strings.Fields(normalizeRaceText(marketText)) {
		w = strings.Trim(w, ".,()'-/&")
		if len(w) < 3 || raceStopWords[w] || raceDateWords[w] || isNumericOrOrdinal(w) {
			continue
		}
		result[w] = true
	}
	return result
}

// raceNamesMatch uses bidirectional word-overlap: returns true if at least 60%
// of either side's significant words appear in the other. This handles sponsor
// prefixes, venue words on one side only, and slight name variations.
func raceNamesMatch(a, b string) bool {
	wordsA := significantRaceWords(a)
	wordsB := significantRaceWords(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}
	matchesAB, matchesBA := 0, 0
	for w := range wordsA {
		if wordsB[w] {
			matchesAB++
		}
	}
	for w := range wordsB {
		if wordsA[w] {
			matchesBA++
		}
	}
	// ceiling of 60% using integer arithmetic: (n*3+4)/5
	threshA := (len(wordsA)*3 + 4) / 5
	threshB := (len(wordsB)*3 + 4) / 5
	return matchesAB >= threshA || matchesBA >= threshB
}

// lookupOddsInState finds a horse's odds on the other bookie, but ONLY within
// a market that matches the same race. Returns "" if the race or horse is not listed.
func lookupOddsInState(state *RunnersState, marketText, horseName string) string {
	key := strings.ToLower(strings.TrimSpace(horseName))
	for _, market := range state.Markets {
		if raceNamesMatch(marketText, market.MarketText) {
			if r, ok := market.Runners[key]; ok && r.Odds != "" {
				return r.Odds
			}
			return "" // race matched, horse not listed
		}
	}
	return "" // race not tracked on other bookie
}

// parseOdds converts a fractional odds string (e.g. "5/1", "7/2", "Evs") to
// a float representing profit per unit stake. Returns -1 if unparseable.
func parseOdds(s string) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "evs", "evens", "1/1":
		return 1.0
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return -1
	}
	num, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	den, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil || den == 0 {
		return -1
	}
	return num / den
}

// movementLabel returns a movement description if odds changed, empty string otherwise.
func movementLabel(oldOdds, newOdds string) string {
	old := parseOdds(oldOdds)
	nw := parseOdds(newOdds)
	if old < 0 || nw < 0 {
		return ""
	}
	if nw < old {
		return " 📉 shortened"
	}
	if nw > old {
		return " 📈 drifted"
	}
	return ""
}

const telegramMaxLen = 3800 // leave headroom under Telegram's 4096 limit

// formatRunnerBlock formats one runner update entry.
func formatRunnerBlock(bookie string, u RunnerUpdate) string {
	if u.IsNew {
		return fmt.Sprintf("\n🆕 %s: %s\n", u.Name, u.NewOdds)
	}
	movement := movementLabel(u.OldOdds, u.NewOdds)
	return fmt.Sprintf("\n🐎 %s%s\n  %s → %s\n", u.Name, movement, u.OldOdds, u.NewOdds)
}

// sendMarketUpdate sends one or more Telegram messages for a market's changes,
// splitting automatically if the content exceeds Telegram's length limit.
func sendMarketUpdate(bookie, marketText, marketURL string, updates []RunnerUpdate) {
	header := fmt.Sprintf("📊 %s\n%s\n%s\n", marketText, bookie, marketURL)

	msg := header
	part := 1

	for _, u := range updates {
		block := formatRunnerBlock(bookie, u)
		if len(msg)+len(block) > telegramMaxLen {
			sendTelegram(msg)
			part++
			msg = fmt.Sprintf("📊 %s (cont. %d)\n%s\n", marketText, part, bookie) + block
		} else {
			msg += block
		}
	}
	if msg != header {
		sendTelegram(msg)
	}
}
