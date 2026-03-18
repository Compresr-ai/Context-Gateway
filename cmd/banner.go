package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ANSI codes for banner styling.
const (
	compresrGreen = "\033[38;2;23;128;68m" // #178044 brand green
	bold          = "\033[1m"
	reset         = "\033[0m"
)

// bannerSplitContext / bannerSplitGateway are the two halves of the banner (~65 cols each).
const bannerSplitContext = `  ██████╗ ██████╗ ███╗  ██╗████████╗███████╗██╗ ██╗████████╗
 ██╔════╝██╔═══██╗████╗ ██║╚══██╔══╝██╔════╝╚██╗██╔╝╚══██╔══╝
 ██║     ██║   ██║██╔██╗██║   ██║   █████╗   ╚███╔╝    ██║
 ██║     ██║   ██║██║╚████║   ██║   ██╔══╝   ██╔██╗    ██║
 ╚██████╗╚██████╔╝██║ ╚███║   ██║   ███████╗██╔╝ ██╗   ██║
  ╚═════╝ ╚═════╝ ╚═╝  ╚══╝   ╚═╝   ╚══════╝╚═╝  ╚═╝   ╚═╝`

const bannerSplitGateway = `  ██████╗  █████╗ ████████╗███████╗██╗    ██╗ █████╗ ██╗   ██╗
 ██╔════╝ ██╔══██╗╚══██╔══╝██╔════╝██║    ██║██╔══██╗╚██╗ ██╔╝
 ██║  ███╗███████║   ██║   █████╗  ██║ █╗ ██║███████║ ╚████╔╝
 ██║   ██║██╔══██║   ██║   ██╔══╝  ██║███╗██║██╔══██║  ╚██╔╝
 ╚██████╔╝██║  ██║   ██║   ███████╗╚███╔███╔╝██║  ██║   ██║
  ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝ ╚══╝╚══╝ ╚═╝  ╚═╝   ╚═╝`

const (
	wideThreshold  = 139 // wide enough for both words side-by-side + gap
	splitThreshold = 65
	wordGap        = 4 // spaces between CONTEXT and GATEWAY in wide mode
)

func terminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd())) // #nosec G115 -- fd value is always a small non-negative integer
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// buildBanner returns the banner string for the given terminal width.
func buildBanner(w int) string {
	switch {
	case w >= wideThreshold:
		return colorLines(buildWideBanner(), w)
	case w >= splitThreshold:
		return colorLines(bannerSplitContext+"\n\n"+bannerSplitGateway, w)
	default:
		const title = "Context Gateway"
		pad := strings.Repeat(" ", max(0, (w-len(title))/2))
		return "\n" + pad + compresrGreen + bold + title + reset + "\n\n"
	}
}

// buildWideBanner joins CONTEXT and GATEWAY side-by-side with wordGap spaces between.
func buildWideBanner() string {
	ctx := strings.Split(bannerSplitContext, "\n")
	gw := strings.Split(bannerSplitGateway, "\n")

	// Width of widest context line (for right-aligning the gap).
	maxCtxW := 0
	for _, l := range ctx {
		if n := len([]rune(l)); n > maxCtxW {
			maxCtxW = n
		}
	}

	var sb strings.Builder
	for i, ctxLine := range ctx {
		gwLine := ""
		if i < len(gw) {
			gwLine = gw[i]
		}
		pad := strings.Repeat(" ", maxCtxW-len([]rune(ctxLine))+wordGap)
		sb.WriteString(ctxLine + pad + gwLine + "\n")
	}
	return sb.String()
}

// colorLines applies green+bold to every line, centred in termW columns.
func colorLines(art string, termW int) string {
	lines := strings.Split(art, "\n")
	maxW := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > maxW {
			maxW = n
		}
	}
	pad := strings.Repeat(" ", max(0, (termW-maxW)/2))
	var sb strings.Builder
	sb.WriteString("\n")
	for _, l := range lines {
		sb.WriteString(pad + compresrGreen + bold + l + reset + "\n")
	}
	return sb.String()
}

// printBanner prints the banner sized to the current terminal width.
func printBanner() {
	fmt.Print(buildBanner(terminalWidth()))
}
