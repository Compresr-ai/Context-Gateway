package tui

// TUI package provides interactive terminal UI components:
//   - Arrow-key menu selection
//   - Interactive prompts
//   - Config creation wizard

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// =============================================================================
// COLORS
// =============================================================================

const (
	ColorReset  = "\033[0m"
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorGreen  = "\033[0;32m"
	ColorBlue   = "\033[0;34m"
	ColorCyan   = "\033[0;36m"
	ColorYellow = "\033[1;33m"
	ColorRed    = "\033[0;31m"
	ColorBrand  = "\033[38;2;23;128;68m" // Compresr brand green
)

// =============================================================================
// PRINT FUNCTIONS
// =============================================================================

// PrintBanner displays the Context Gateway ASCII banner.
func PrintBanner() {
	fmt.Printf("%s%s", ColorBrand, ColorBold)
	fmt.Println(`
  ██████╗ ██████╗ ███╗  ██╗████████╗███████╗██╗ ██╗████████╗  ██████╗  █████╗ ████████╗███████╗██╗    ██╗ █████╗ ██╗   ██╗
 ██╔════╝██╔═══██╗████╗ ██║╚══██╔══╝██╔════╝╚██╗██╔╝╚══██╔══╝ ██╔════╝ ██╔══██╗╚══██╔══╝██╔════╝██║    ██║██╔══██╗╚██╗ ██╔╝
 ██║     ██║   ██║██╔██╗██║   ██║   █████╗   ╚███╔╝    ██║    ██║  ███╗███████║   ██║   █████╗  ██║ █╗ ██║███████║ ╚████╔╝
 ██║     ██║   ██║██║╚████║   ██║   ██╔══╝   ██╔██╗    ██║    ██║   ██║██╔══██║   ██║   ██╔══╝  ██║███╗██║██╔══██║  ╚██╔╝
 ╚██████╗╚██████╔╝██║ ╚███║   ██║   ███████╗██╔╝ ██╗   ██║    ╚██████╔╝██║  ██║   ██║   ███████╗╚███╔███╔╝██║  ██║   ██║
  ╚═════╝ ╚═════╝ ╚═╝  ╚══╝   ╚═╝   ╚══════╝╚═╝  ╚═╝   ╚═╝     ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝ ╚══╝╚══╝ ╚═╝  ╚═╝   ╚═╝`)
	fmt.Print(ColorReset)
}

// PrintHeader prints a styled section header.
func PrintHeader(title string) {
	fmt.Printf("\n%s%s========================================%s\n", ColorBold, ColorCyan, ColorReset)
	fmt.Printf("%s%s       %s%s\n", ColorBold, ColorCyan, title, ColorReset)
	fmt.Printf("%s%s========================================%s\n\n", ColorBold, ColorCyan, ColorReset)
}

// PrintSuccess prints a success message with green [OK] prefix.
func PrintSuccess(msg string) {
	fmt.Printf("%s[OK]%s %s\n", ColorGreen, ColorReset, msg)
}

// PrintInfo prints an info message with blue [INFO] prefix.
func PrintInfo(msg string) {
	fmt.Printf("%s[INFO]%s %s\n", ColorBlue, ColorReset, msg)
}

// PrintWarn prints a warning message with yellow [WARN] prefix.
func PrintWarn(msg string) {
	fmt.Printf("%s[WARN]%s %s\n", ColorYellow, ColorReset, msg)
}

// PrintError prints an error message with red [ERROR] prefix.
func PrintError(msg string) {
	fmt.Printf("%s[ERROR]%s %s\n", ColorRed, ColorReset, msg)
}

// PrintStep prints a step/action message with cyan >>> prefix.
func PrintStep(msg string) {
	fmt.Printf("%s>>>%s %s\n", ColorCyan, ColorReset, msg)
}

// SetTerminalTitle sets the terminal window/tab title using OSC escape sequence.
// This persists across scrolling, keeping status info always visible.
func SetTerminalTitle(title string) {
	fmt.Printf("\033]0;%s\007", title)
}

// ClearTerminalTitle resets the terminal title to default.
func ClearTerminalTitle() {
	fmt.Print("\033]0;\007")
}

// =============================================================================
// MENU SELECTION
// =============================================================================

// MenuItem represents an item in a menu.
type MenuItem struct {
	Label        string // Display label
	Description  string // Optional description (or current value for editable)
	Value        string // Return value (if different from label)
	Editable     bool   // If true, allows inline text editing
	Locked       bool   // If true, item cannot be selected (grayed out with lock icon)
	LockedReason string // Reason shown when item is locked (e.g., "Requires Pro subscription")
}

// menuLines tracks how many lines the last menu used (for clearing)
var menuLines int

// ClearLastMenu clears the lines used by the previous menu
func ClearLastMenu() {
	if menuLines > 0 {
		// Move up and clear each line
		for i := 0; i < menuLines; i++ {
			fmt.Print("\033[A\033[2K") // Move up, clear line
		}
		fmt.Print("\r")
		menuLines = 0
	}
}

// SelectMenu displays an interactive arrow-key menu and returns the selected index.
// Returns -1 and error if cancelled.
func SelectMenu(prompt string, items []MenuItem) (int, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("no items to select")
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return selectNumberedMenu(prompt, items)
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return selectNumberedMenu(prompt, items)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	selected := 0
	reader := bufio.NewReader(os.Stdin)

	// Get terminal width for truncating descriptions
	termWidth, _, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil || termWidth < 40 {
		termWidth = 80 // Default fallback
	}

	// Calculate total lines we'll render
	totalLines := 3 + len(items) + 2 // prompt + blank + items + blank + help

	// Hide cursor
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h") // Show cursor on exit

	firstRender := true

	// Helper to truncate text to fit terminal width
	truncate := func(text string, maxLen int) string {
		if len(text) <= maxLen {
			return text
		}
		if maxLen < 3 {
			return text[:maxLen]
		}
		return text[:maxLen-3] + "..."
	}

	renderMenu := func() {
		if !firstRender {
			// Move cursor up to start of menu and clear
			fmt.Printf("\033[%dA", totalLines)
		}
		firstRender = false

		// Clear line and print prompt
		fmt.Print("\033[2K") // Clear line
		fmt.Printf("\r\n%s%s%s%s\n\n", ColorBold, ColorCyan, prompt, ColorReset)

		for i, item := range items {
			fmt.Print("\033[2K") // Clear line

			// Calculate max description length to prevent wrapping
			// Account for: "  ❯ " (4) + label + " - " (3) + description
			prefixLen := 4
			if i != selected {
				prefixLen = 4 // "    " (no arrow)
			}
			maxDescLen := termWidth - prefixLen - len(item.Label) - 3 - 10 // -10 for safety margin
			if maxDescLen < 20 {
				maxDescLen = 20 // Minimum description length
			}

			if item.Locked {
				// Locked items are grayed out with lock icon
				desc := item.Description
				if item.LockedReason != "" {
					desc = "[" + item.LockedReason + "]"
				}
				if desc != "" {
					desc = truncate(desc, maxDescLen)
				}

				if i == selected {
					fmt.Printf("\r  %s❯%s %s🔒 %s%s", ColorDim, ColorReset, ColorDim, item.Label, ColorReset)
				} else {
					fmt.Printf("\r    %s🔒 %s%s", ColorDim, item.Label, ColorReset)
				}
				if desc != "" {
					fmt.Printf(" %s%s%s", ColorDim, desc, ColorReset)
				}
			} else if i == selected {
				desc := item.Description
				if desc != "" {
					desc = truncate(desc, maxDescLen)
					desc = " " + ColorDim + "- " + desc + ColorReset
				}
				fmt.Printf("\r  %s❯%s %s%s%s%s", ColorGreen, ColorReset, ColorBold, item.Label, ColorReset, desc)
			} else {
				desc := item.Description
				if desc != "" {
					desc = truncate(desc, maxDescLen)
					desc = " " + ColorDim + "- " + desc + ColorReset
				}
				fmt.Printf("\r    %s%s", item.Label, desc)
			}
			fmt.Print("\n")
		}
		fmt.Print("\033[2K") // Clear line
		fmt.Printf("\r\n  %s[↑/↓] Navigate  [Enter] Select  [q/Esc] Cancel%s\n", ColorDim, ColorReset)
	}

	renderMenu()

	for {
		b, err := reader.ReadByte()
		if err != nil {
			return -1, err
		}

		switch b {
		case 3: // Ctrl+C - exit immediately
			// Restore terminal state before exiting
			fmt.Print("\033[?25h") // Show cursor
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			fmt.Println("\n\nInterrupted.")
			os.Exit(130) // Standard exit code for Ctrl+C

		case 'q', 27: // q or Escape
			// Check for escape sequence (arrow keys)
			if b == 27 {
				// Read next two bytes for escape sequence
				next, _ := reader.ReadByte()
				if next == '[' {
					arrow, _ := reader.ReadByte()
					switch arrow {
					case 'A': // Up arrow
						if selected > 0 {
							selected--
						}
						renderMenu()
						continue
					case 'B': // Down arrow
						if selected < len(items)-1 {
							selected++
						}
						renderMenu()
						continue
					}
				}
				// Pure Escape key - cancel
				// Clear menu before exit
				fmt.Printf("\033[%dA", totalLines)
				for i := 0; i < totalLines; i++ {
					fmt.Print("\033[2K\n")
				}
				fmt.Printf("\033[%dA", totalLines)
				return -1, fmt.Errorf("cancelled")
			}
			// 'q' - cancel
			fmt.Printf("\033[%dA", totalLines)
			for i := 0; i < totalLines; i++ {
				fmt.Print("\033[2K\n")
			}
			fmt.Printf("\033[%dA", totalLines)
			return -1, fmt.Errorf("cancelled")
		case 'k': // vim-style up
			if selected > 0 {
				selected--
			}
			renderMenu()
		case 'j': // vim-style down
			if selected < len(items)-1 {
				selected++
			}
			renderMenu()
		case 13: // Enter
			// Check if this is a locked item - don't allow selection
			if items[selected].Locked {
				// Show a brief message and continue
				continue
			}
			// Check if this is an editable item
			if items[selected].Editable {
				// Calculate position: from help line, go up to selected item
				// Help line is at bottom, items are above it (with 1 blank line between)
				// totalLines = 3 + len(items) + 2 = prompt(1) + blank(2) + items + blank(1) + help(1)
				linesUp := (len(items) - selected) + 2 // +2 for blank line and help line

				// Move up to the selected item line
				fmt.Printf("\033[%dA", linesUp)
				fmt.Print("\033[2K\r")

				_ = term.Restore(int(os.Stdin.Fd()), oldState)
				fmt.Print("\033[?25h")

				// Show editable line with cursor after dash
				fmt.Printf("  %s❯%s %s%s%s - ", ColorGreen, ColorReset, ColorBold, items[selected].Label, ColorReset)

				inputReader := bufio.NewReader(os.Stdin)
				input, _ := inputReader.ReadString('\n')
				input = strings.TrimSpace(input)

				if input != "" {
					items[selected].Description = input
				}

				oldState, _ = term.MakeRaw(int(os.Stdin.Fd()))
				fmt.Print("\033[?25l")

				// Now we're on line below the edited item (Enter moved us down)
				// Move back up to the edited line (we're 1 below it after Enter)
				fmt.Print("\033[1A")
				// Re-draw the edited line with updated value
				fmt.Print("\033[2K\r")
				fmt.Printf("  %s❯%s %s%s%s", ColorGreen, ColorReset, ColorBold, items[selected].Label, ColorReset)
				if items[selected].Description != "" {
					fmt.Printf(" %s- %s%s", ColorDim, items[selected].Description, ColorReset)
				}

				// Move down and redraw remaining items
				fmt.Println()
				for i := selected + 1; i < len(items); i++ {
					fmt.Print("\033[2K\r")
					fmt.Printf("    %s", items[i].Label)
					if items[i].Description != "" {
						fmt.Printf(" %s- %s%s", ColorDim, items[i].Description, ColorReset)
					}
					fmt.Println()
				}

				// Skip past the existing blank line and help line (they're already rendered)
				// Just move cursor down 2 lines to end position
				fmt.Print("\033[2B")

				continue
			}

			// Non-editable item - return silently (no confirmation printed)
			// Just clear the menu and return
			fmt.Printf("\033[%dA", totalLines)
			for i := 0; i < totalLines; i++ {
				fmt.Print("\033[2K\n")
			}
			fmt.Printf("\033[%dA", totalLines)
			menuLines = 0
			return selected, nil
		}
	}
}

// selectNumberedMenu is a fallback for non-interactive terminals.
func selectNumberedMenu(prompt string, items []MenuItem) (int, error) {
	fmt.Printf("\n%s%s%s%s\n\n", ColorBold, ColorCyan, prompt, ColorReset)

	for i, item := range items {
		fmt.Printf("  %s[%d]%s %s", ColorGreen, i+1, ColorReset, item.Label)
		if item.Description != "" {
			fmt.Printf(" %s- %s%s", ColorDim, item.Description, ColorReset)
		}
		fmt.Println()
	}
	fmt.Printf("  %s[0]%s Cancel\n\n", ColorYellow, ColorReset)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter number: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "0" || input == "q" {
			return -1, fmt.Errorf("cancelled")
		}

		var num int
		if _, err := fmt.Sscanf(input, "%d", &num); err == nil {
			if num >= 1 && num <= len(items) {
				return num - 1, nil
			}
		}
		fmt.Printf("Invalid choice. Enter 1-%d or 0 to cancel.\n", len(items))
	}
}

// =============================================================================
// PROMPTS
// =============================================================================

// PromptString prompts for a string input. Returns empty if skipped.
func PromptString(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// PromptYesNo prompts for a yes/no response. Returns the default if empty.
func PromptYesNo(prompt string, defaultYes bool) bool {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	fmt.Print(prompt + suffix)

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}

// PromptPassword prompts for a password (hidden input).
func PromptPassword(prompt string) string {
	fmt.Print(prompt)

	if term.IsTerminal(int(os.Stdin.Fd())) {
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(password))
		}
	}

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// PromptInput prompts for visible text input (for API keys, etc.).
func PromptInput(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// ReadLine reads a line of input (for inline editing without new page).
func ReadLine() string {
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}
