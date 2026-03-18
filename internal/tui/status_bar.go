package tui

// StatusBar provides a persistent status display for gateway usage and balance.
//
// Features:
// - Shows credits remaining, tier, and usage percentage
// - Automatic refresh every N requests or M duration
// - Color-coded warnings at low balance

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"golang.org/x/term"
)

// CONFIGURATION

const (
	// RefreshInterval is the minimum time between status refreshes
	RefreshInterval = 5 * time.Minute

	// RefreshRequestCount triggers refresh after this many requests
	RefreshRequestCount = 5

	// LowBalanceThreshold triggers warning when credits below this USD
	LowBalanceThreshold = 5.0

	// CriticalBalanceThreshold triggers critical warning
	CriticalBalanceThreshold = 1.0

	// AutoRefreshInterval keeps the footer updated even without requests
	AutoRefreshInterval = 10 * time.Second
)

// COST SOURCE INTERFACE

// CostSource provides local session cost data for the status bar.
// Implemented by costcontrol.Tracker to avoid circular imports.
type CostSource interface {
	GetGlobalCost() float64
	GetGlobalCap() float64
}

// SavingsSource provides compression savings data for the status bar.
// Implemented by monitoring.SavingsTracker to avoid circular imports.
type SavingsSource interface {
	GetSavingsSummary() (tokensSaved int, costSavedUSD float64, compressedRequests int, totalRequests int)
	GetCostBreakdown() (originalCostUSD float64, compressedCostUSD float64, costSavedUSD float64)
	GetCompressionStats() (compressedRequests int, totalRequests int, toolDiscoveryRequests int, originalToolCount int, filteredToolCount int)
}

// ExpandSource provides expand_context stats for the status bar.
// Implemented by monitoring.ExpandLog to avoid circular imports.
type ExpandSource interface {
	GetExpandSummary() (total int, found int, notFound int)
}

// STATUS BAR

// StatusBar manages and displays the gateway usage status.
type StatusBar struct {
	mu sync.RWMutex

	// Cached status
	status *compresr.GatewayStatus

	// Refresh tracking
	lastRefresh  time.Time
	requestCount int

	// API client
	client *compresr.Client

	// Local cost source (optional)
	costSource CostSource

	// Savings source (optional)
	savingsSource SavingsSource

	// Expand context source (optional)
	expandSource ExpandSource

	// Dashboard port (for displaying URL)
	dashboardPort int

	// Session name (for terminal title display)
	sessionName string

	// Display configuration
	enabled bool

	// Persistent footer control
	footerEnabled bool
	autoRefreshOn bool
	autoStop      chan struct{}
}

// NewStatusBar creates a new status bar with the given API client.
func NewStatusBar(client *compresr.Client) *StatusBar {
	return &StatusBar{
		client:        client,
		enabled:       client != nil && client.HasAPIKey(),
		footerEnabled: false,
	}
}

// PUBLIC METHODS

// Refresh fetches the latest status from the API, bypassing the client cache.
// Use this for explicit refresh calls (startup, exit) where fresh data is needed.
// Returns error if the fetch fails.
func (sb *StatusBar) Refresh() error {
	if !sb.enabled || sb.client == nil {
		return nil
	}

	status, err := sb.client.GetGatewayStatusFresh()
	if err != nil {
		return err
	}

	sb.mu.Lock()
	sb.status = status
	sb.lastRefresh = time.Now()
	sb.requestCount = 0
	sb.mu.Unlock()

	return nil
}

// IncrementRequests increments the request counter.
// Call this after each compression request.
func (sb *StatusBar) IncrementRequests() {
	sb.mu.Lock()
	sb.requestCount++
	sb.mu.Unlock()
}

// ShouldRefresh returns true if the status should be refreshed.
func (sb *StatusBar) ShouldRefresh() bool {
	if !sb.enabled {
		return false
	}

	sb.mu.RLock()
	defer sb.mu.RUnlock()

	// Refresh if never fetched
	if sb.status == nil || sb.lastRefresh.IsZero() {
		return true
	}

	// Refresh if enough requests have been made
	if sb.requestCount >= RefreshRequestCount {
		return true
	}

	// Refresh if enough time has passed
	if time.Since(sb.lastRefresh) >= RefreshInterval {
		return true
	}

	return false
}

// MaybeRefresh refreshes the status if needed and re-renders.
// Call this after each request to potentially update the display.
func (sb *StatusBar) MaybeRefresh() bool {
	if !sb.ShouldRefresh() {
		return false
	}

	if err := sb.Refresh(); err != nil {
		// Don't fail silently - show warning but continue
		return false
	}

	sb.Render()
	return true
}

// MaybeRefreshCompact refreshes the status if needed and renders a compact line.
// Call this after each request to keep the display updated without clutter.
func (sb *StatusBar) MaybeRefreshCompact() bool {
	if !sb.ShouldRefresh() {
		return false
	}

	if err := sb.Refresh(); err != nil {
		return false
	}

	sb.RenderCompact()
	return true
}

// GetStatus returns the cached status (may be nil).
func (sb *StatusBar) GetStatus() *compresr.GatewayStatus {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.status
}

// SetCostSource attaches a local cost data source for session spend display.
func (sb *StatusBar) SetCostSource(cs CostSource) {
	sb.mu.Lock()
	sb.costSource = cs
	sb.mu.Unlock()
}

// SetSavingsSource attaches a savings data source for exit summary display.
func (sb *StatusBar) SetSavingsSource(ss SavingsSource) {
	sb.mu.Lock()
	sb.savingsSource = ss
	sb.mu.Unlock()
}

// SetExpandSource attaches an expand context data source for stats display.
func (sb *StatusBar) SetExpandSource(es ExpandSource) {
	sb.mu.Lock()
	sb.expandSource = es
	sb.mu.Unlock()
}

// SetDashboardPort sets the port for dashboard URL display.
func (sb *StatusBar) SetDashboardPort(port int) {
	sb.mu.Lock()
	sb.dashboardPort = port
	sb.mu.Unlock()
}

// EnableFooter toggles the persistent footer line.
func (sb *StatusBar) EnableFooter(enabled bool) {
	sb.mu.Lock()
	sb.footerEnabled = enabled
	sb.mu.Unlock()
}

// StartAutoRefresh starts a periodic refresh that updates the footer.
// Safe to call multiple times; subsequent calls are ignored.
func (sb *StatusBar) StartAutoRefresh(interval time.Duration) {
	if interval <= 0 {
		return
	}
	stdoutFd := int(os.Stdout.Fd()) // #nosec G115 -- fd fits in int on all supported platforms
	if !term.IsTerminal(stdoutFd) {
		return
	}

	sb.mu.Lock()
	if sb.autoRefreshOn {
		sb.mu.Unlock()
		return
	}
	sb.autoRefreshOn = true
	sb.autoStop = make(chan struct{})
	stopCh := sb.autoStop
	sb.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := sb.Refresh(); err == nil {
					sb.RenderFooter()
				}
			case <-stopCh:
				return
			}
		}
	}()
}

// StopAutoRefresh stops the periodic refresh loop if running.
func (sb *StatusBar) StopAutoRefresh() {
	sb.mu.Lock()
	if !sb.autoRefreshOn {
		sb.mu.Unlock()
		return
	}
	sb.autoRefreshOn = false
	if sb.autoStop != nil {
		close(sb.autoStop)
		sb.autoStop = nil
	}
	sb.mu.Unlock()
}

// SetSessionName sets the session name for terminal title display.
func (sb *StatusBar) SetSessionName(name string) {
	sb.mu.Lock()
	sb.sessionName = name
	sb.mu.Unlock()
}

// RENDERING

// Render prints the status bar to stdout.
func (sb *StatusBar) Render() {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	if !sb.enabled || sb.status == nil {
		return
	}

	fmt.Println(sb.formatStatusLine())
}

// RenderCompact updates the terminal title with current status.
// Uses OSC escape sequence which doesn't conflict with agent TUI output.
func (sb *StatusBar) RenderCompact() {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	if !sb.enabled || sb.status == nil {
		return
	}

	// Update terminal title (non-intrusive, won't conflict with agent TUI)
	SetTerminalTitle(sb.formatTitleStatusLocked())
}

// formatTitleStatusLocked returns the title string (caller must hold mu.RLock).
func (sb *StatusBar) formatTitleStatusLocked() string {
	base := fmt.Sprintf("Context Gateway | :%d", sb.dashboardPort)
	if sb.sessionName != "" {
		base += " | " + sb.sessionName
	}

	if !sb.enabled || sb.status == nil {
		return base
	}

	s := sb.status
	if s.IsAdmin {
		return fmt.Sprintf("%s | unlimited | %s", base, formatTier(s.Tier))
	}
	return fmt.Sprintf("%s | $%.2f | %s", base, s.CreditsRemainingUSD, formatTier(s.Tier))
}

// RenderBox prints the dashboard URL at startup.
func (sb *StatusBar) RenderBox() {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	// Dashboard URL only
	sb.renderDashboardURL()
}

// RenderStartup prints a clean startup block with name, port, plan, and session info.
func (sb *StatusBar) RenderStartup(sessionName string) {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	// Context Gateway header
	fmt.Printf("\n  %s%sContext Gateway%s", ColorBrand, ColorBold, ColorReset)
	if sb.dashboardPort > 0 {
		fmt.Printf("  %s:%d%s", ColorDim, sb.dashboardPort, ColorReset)
	}
	fmt.Println()

	// Session
	if sessionName != "" {
		fmt.Printf("  %s✓%s %sSession:%s %s\n", ColorGreen, ColorReset, ColorDim, ColorReset, sessionName)
	}
}

// renderDashboardURL prints the dashboard URL using the configured port.
func (sb *StatusBar) renderDashboardURL() {
	if sb.dashboardPort > 0 {
		fmt.Printf("  %s✓%s %sDashboard:%s http://localhost:%d/dashboard/\n",
			ColorGreen, ColorReset, ColorDim, ColorReset, sb.dashboardPort)
	}
}

// RenderSummary prints plan, credits, session spend, and savings (for exit summary).
func (sb *StatusBar) RenderSummary() {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	// Plan + credits (refreshed)
	if sb.enabled && sb.status != nil {
		s := sb.status
		tier := formatTier(s.Tier)
		fmt.Printf("  %s·%s Plan: %s%s%s\n", ColorDim, ColorReset, ColorBold, tier, ColorReset)

		if s.Tier == "enterprise" || s.IsAdmin {
			fmt.Printf("  %s·%s Usage this month: $%.2f (pay-as-you-go)\n",
				ColorDim, ColorReset, s.CreditsUsedThisMonth)
		} else {
			balanceColor := getBalanceColor(s.CreditsRemainingUSD)
			if s.MonthlyBudgetUSD > 0 {
				totalCredits := s.CreditsRemainingUSD + s.CreditsUsedThisMonth
				fmt.Printf("  %s·%s Credits: %s$%.2f%s / $%.2f\n",
					ColorDim, ColorReset,
					balanceColor, s.CreditsRemainingUSD, ColorReset,
					totalCredits)
			} else {
				fmt.Printf("  %s·%s Credits: %s$%.2f%s\n",
					ColorDim, ColorReset,
					balanceColor, s.CreditsRemainingUSD, ColorReset)
			}
		}
	}

	// Session spend
	if sb.costSource != nil {
		globalCost := sb.costSource.GetGlobalCost()
		globalCap := sb.costSource.GetGlobalCap()
		if globalCap > 0 {
			fmt.Printf("  %s·%s Session spend: $%.4f / $%.2f\n",
				ColorDim, ColorReset, globalCost, globalCap)
		} else {
			fmt.Printf("  %s·%s Session spend: $%.4f\n",
				ColorDim, ColorReset, globalCost)
		}
	}
}

// RenderFooter paints the compact line as a persistent footer.
func (sb *StatusBar) RenderFooter() {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	sb.renderFooterLocked()
}

// renderFooterLocked renders the compact line as a persistent footer.
// Caller must hold a read lock.
func (sb *StatusBar) renderFooterLocked() {
	if !sb.enabled || sb.status == nil || !sb.footerEnabled {
		return
	}
	stdoutFd := int(os.Stdout.Fd()) // #nosec G115 -- fd fits in int on all supported platforms
	if !term.IsTerminal(stdoutFd) {
		return
	}

	line := sb.formatCompactLine()
	if line == "" {
		return
	}

	// Save cursor, move to bottom line, clear, print, restore.
	// Use DECSC/DECRC for broad terminal compatibility.
	fmt.Print("\0337")
	if _, h, err := term.GetSize(stdoutFd); err == nil && h > 0 {
		fmt.Printf("\033[%d;1H", h)
	} else {
		fmt.Print("\r")
	}
	fmt.Print("\033[2K")
	fmt.Printf("  %s", line)
	fmt.Print("\0338")
}

// formatStatusLine returns a formatted status line string.
func (sb *StatusBar) formatStatusLine() string {
	s := sb.status
	if s == nil {
		return ""
	}

	balanceColor := getBalanceColor(s.CreditsRemainingUSD)

	tier := formatTier(s.Tier)
	refreshAgo := formatDuration(time.Since(sb.lastRefresh))

	if s.IsAdmin {
		return fmt.Sprintf("  💳 %s∞ unlimited%s │ %s%s%s │ ↻ %s",
			balanceColor, ColorReset,
			ColorBold, tier, ColorReset,
			refreshAgo)
	}

	totalCredits, usedPercent, remainingPercent := calcCreditPercents(s)

	line := fmt.Sprintf("  💳 %s$%.2f%s / $%.2f %s %.0f%% left │ %s%s%s │ ↻ %s",
		balanceColor, s.CreditsRemainingUSD, ColorReset,
		totalCredits, renderMiniBar(usedPercent, 12), remainingPercent,
		ColorBold, tier, ColorReset,
		refreshAgo)

	// Append local spend if available
	if sb.costSource != nil {
		globalCost := sb.costSource.GetGlobalCost()
		globalCap := sb.costSource.GetGlobalCap()
		if globalCap > 0 {
			spendPercent := (globalCost / globalCap) * 100
			if spendPercent > 100 {
				spendPercent = 100
			}
			line += fmt.Sprintf(" │ 💰 $%.2f/$%.2f %s",
				globalCost, globalCap, renderMiniBar(spendPercent, 8))
		} else {
			line += fmt.Sprintf(" │ 💰 $%.4f spent", globalCost)
		}
	}

	// Append savings if available
	// if sb.savingsSource != nil {
	// 	tokensSaved, costSaved, _, _ := sb.savingsSource.GetSavingsSummary()
	// 	if tokensSaved > 0 {
	// 		line += fmt.Sprintf(" │ %s↓ %d tok saved ($%.4f)%s", ColorGreen, tokensSaved, costSaved, ColorReset)
	// 	}
	// }

	return line
}

// formatCompactLine returns a compact status string for inline display.
func (sb *StatusBar) formatCompactLine() string {
	s := sb.status
	if s == nil {
		return ""
	}

	balanceColor := getBalanceColor(s.CreditsRemainingUSD)

	if s.IsAdmin {
		return fmt.Sprintf("%s[Admin: unlimited]%s", ColorDim, ColorReset)
	}

	// Credits with mini bar — remaining / total
	totalCredits, usedPercent, remainingPercent := calcCreditPercents(s)

	creditsPart := fmt.Sprintf("Credits: %s$%.2f%s%s/$%.2f %s %.0f%% left",
		balanceColor, s.CreditsRemainingUSD, ColorReset, ColorDim,
		totalCredits,
		renderMiniBar(usedPercent, 10), remainingPercent)

	// Local spend (if cost source is wired)
	spendPart := ""
	if sb.costSource != nil {
		globalCost := sb.costSource.GetGlobalCost()
		globalCap := sb.costSource.GetGlobalCap()
		if globalCap > 0 {
			spendPercent := (globalCost / globalCap) * 100
			if spendPercent > 100 {
				spendPercent = 100
			}
			spendPart = fmt.Sprintf(" │ Spend: $%.2f/$%.2f %s %.0f%%",
				globalCost, globalCap,
				renderMiniBar(spendPercent, 8), spendPercent)
		} else {
			spendPart = fmt.Sprintf(" │ Spend: $%.4f", globalCost)
		}
	}

	// Savings (if available)
	savingsPart := ""
	// if sb.savingsSource != nil {
	// 	tokensSaved, costSaved, _, _ := sb.savingsSource.GetSavingsSummary()
	// 	if tokensSaved > 0 {
	// 		savingsPart = fmt.Sprintf(" │ %sSaved: %d tok ($%.4f)%s",
	// 			ColorGreen, tokensSaved, costSaved, ColorReset)
	// 	}
	// }

	return fmt.Sprintf("%s[%s%s%s]%s", ColorDim, creditsPart, spendPart, savingsPart, ColorReset)
}

// FormatTitleStatus returns a plain-text status string for the terminal title bar.
// Format: "Context Gateway | port:18081 | $12.50 | Pro"
// This version takes explicit port/session args for initial display before fields are set.
func (sb *StatusBar) FormatTitleStatus(port int, session string) string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	// Use explicit args if provided, otherwise fall back to stored fields
	p := port
	if p == 0 {
		p = sb.dashboardPort
	}
	s := session
	if s == "" {
		s = sb.sessionName
	}

	base := fmt.Sprintf("Context Gateway | :%d", p)
	if s != "" {
		base += " | " + s
	}

	if !sb.enabled || sb.status == nil {
		return base
	}

	st := sb.status
	if st.IsAdmin {
		return fmt.Sprintf("%s | unlimited | %s", base, formatTier(st.Tier))
	}
	return fmt.Sprintf("%s | $%.2f remaining | %s", base, st.CreditsRemainingUSD, formatTier(st.Tier))
}

// PROGRESS BAR

// renderMiniBar returns a compact bar without brackets for inline display.
// width is the number of bar characters.
func renderMiniBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := int(percent / 100 * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	barColor := ColorGreen
	if percent >= 80 {
		barColor = ColorRed
	} else if percent >= 50 {
		barColor = ColorYellow
	}

	bar := barColor
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	bar += ColorReset + ColorDim
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	bar += ColorReset

	return bar
}

// HELPERS

// getBalanceColor returns the appropriate color for the given credit balance.
func getBalanceColor(credits float64) string {
	if credits < CriticalBalanceThreshold {
		return ColorRed
	}
	if credits < LowBalanceThreshold {
		return ColorYellow
	}
	return ColorGreen
}

// calcCreditPercents computes total credits, used percentage, and remaining percentage.
func calcCreditPercents(s *compresr.GatewayStatus) (totalCredits, usedPercent, remainingPercent float64) {
	totalCredits = s.CreditsRemainingUSD + s.CreditsUsedThisMonth
	if totalCredits > 0 {
		usedPercent = (s.CreditsUsedThisMonth / totalCredits) * 100
	}
	remainingPercent = 100 - usedPercent
	return
}

func formatTier(tier string) string {
	switch tier {
	case "free":
		return "Free"
	case "pro":
		return "Pro"
	case "business":
		return "Business"
	case "enterprise":
		return "Enterprise"
	default:
		return tier
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
