// Terminal window focus — brings the terminal running a gateway instance to the foreground.
//
// DESIGN: Uses TERM_PROGRAM (captured at registration) to know which terminal app to activate.
// For iTerm2, uses AppleScript to find the specific tab by TTY device.
// For other terminals, activates the app (which brings the last-used window to front).
package dashboard

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FocusTerminal brings the terminal window for the given instance to the foreground.
func FocusTerminal(inst Instance) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("terminal focus only supported on macOS")
	}
	return focusMacOS(inst)
}

func focusMacOS(inst Instance) error {
	appName := resolveAppName(inst.TermProgram)
	if appName == "" {
		return fmt.Errorf("unknown terminal: %s", inst.TermProgram)
	}

	// For iTerm2, try to find and raise the specific tab by TTY
	if strings.Contains(strings.ToLower(appName), "iterm") && inst.TTY != "" {
		ttyName := filepath.Base(inst.TTY) // e.g. "ttys003"
		// This script:
		// 1. Iterates all windows/tabs/sessions to find the matching TTY
		// 2. Selects the tab (makes it the active tab in its window)
		// 3. Sets the window to frontmost and uses AXRaise to bring it to front
		// 4. Activates the app (switches to its Space/Desktop if needed)
		script := fmt.Sprintf(`
tell application "iTerm2"
	repeat with w in windows
		tell w
			repeat with t in tabs
				tell t
					repeat with s in sessions
						tell s
							if (tty) contains "%s" then
								select
							end if
						end tell
					end repeat
				end tell
				-- Check if we found it: current session's tty should match
				try
					if (tty of current session of current tab of w) contains "%s" then
						select t
						tell application "System Events"
							tell process "iTerm2"
								perform action "AXRaise" of window (name of w)
							end tell
						end tell
						activate
						return "found"
					end if
				end try
			end repeat
		end tell
	end repeat
	activate
end tell
return "not_found"`, ttyName, ttyName)

		cmd := exec.Command("osascript", "-e", script) // #nosec G204 -- controlled AppleScript
		if out, err := cmd.CombinedOutput(); err == nil && strings.Contains(string(out), "found") {
			return nil
		}
		// Fall through to simple activate if tab-specific selection failed
	}

	// General approach: use System Events to raise the window + activate the app.
	// `activate` alone may not switch to a different Desktop/Space.
	// Using `set frontmost to true` via System Events forces the window switch.
	script := fmt.Sprintf(`
tell application "System Events"
	tell process "%s"
		set frontmost to true
	end tell
end tell
tell application "%s" to activate`, appName, appName)
	cmd := exec.Command("osascript", "-e", script) // #nosec G204 -- controlled AppleScript
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveAppName maps TERM_PROGRAM env values to macOS app names.
func resolveAppName(termProgram string) string {
	tp := strings.ToLower(termProgram)
	switch {
	case strings.Contains(tp, "iterm"):
		return "iTerm2"
	case tp == "apple_terminal":
		return "Terminal"
	case strings.Contains(tp, "warp"):
		return "Warp"
	case strings.Contains(tp, "alacritty"):
		return "Alacritty"
	case strings.Contains(tp, "kitty"):
		return "kitty"
	case strings.Contains(tp, "hyper"):
		return "Hyper"
	case strings.Contains(tp, "vscode"):
		return "Visual Studio Code"
	case strings.Contains(tp, "cursor"):
		return "Cursor"
	case tp == "":
		return "Terminal" // default fallback
	default:
		return termProgram
	}
}
