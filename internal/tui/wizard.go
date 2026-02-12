package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// =============================================================================
// WIZARD FORM - Single-page multi-field form
// =============================================================================

// FieldType represents the type of a wizard field.
type FieldType int

const (
	FieldTypeSelect   FieldType = iota // Arrow-key selection from options
	FieldTypeYesNo                     // Yes/No selection
	FieldTypeText                      // Text input
	FieldTypePassword                  // Hidden text input
)

// WizardField represents a single field in the wizard form.
type WizardField struct {
	ID          string     // Unique identifier
	Label       string     // Display label
	Description string     // Optional help text
	Type        FieldType  // Field type
	Options     []MenuItem // For FieldTypeSelect
	Required    bool       // Whether field is required
	Value       string     // Current value (display string)
	ValueIndex  int        // For select fields, the selected index
	Skip        bool       // Whether to skip this field
}

// WizardResult contains all field values after wizard completion.
type WizardResult struct {
	Values map[string]interface{} // Field ID -> value
}

// RunWizard displays a single-page wizard form with all fields visible.
// Users can navigate between fields with arrow keys and edit each field.
func RunWizard(title string, fields []WizardField) (*WizardResult, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("no fields provided")
	}

	// Filter out skipped fields
	var activeFields []WizardField
	for _, f := range fields {
		if !f.Skip {
			activeFields = append(activeFields, f)
		}
	}
	if len(activeFields) == 0 {
		return nil, fmt.Errorf("all fields skipped")
	}

	// Check if we're in a TTY
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return runWizardFallback(title, activeFields)
	}

	// Save terminal state and set raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return runWizardFallback(title, activeFields)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	current := 0 // Current field index
	editing := false
	editSelected := 0 // For select fields, which option is highlighted while editing

	reader := bufio.NewReader(os.Stdin)

	// Initialize field options for YesNo fields
	for i := range activeFields {
		if activeFields[i].Type == FieldTypeYesNo && len(activeFields[i].Options) == 0 {
			activeFields[i].Options = []MenuItem{
				{Label: "Yes", Value: "yes"},
				{Label: "No", Value: "no"},
			}
		}
	}

	// Fixed layout: each field gets 2 lines (label + value), plus header and footer
	// Total = 3 (header) + fields*2 + 2 (footer)
	maxLines := 3 + len(activeFields)*2 + 2

	// Hide cursor
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")

	firstRender := true

	renderWizard := func() {
		if !firstRender {
			// Move cursor up to start of wizard
			fmt.Printf("\033[%dA", maxLines)
		}
		firstRender = false

		// Clear and print title
		fmt.Print("\033[2K\r")
		fmt.Printf("%s%s%s%s\n", ColorBold, ColorCyan, title, ColorReset)
		fmt.Print("\033[2K\r")
		fmt.Printf("%s%s%s\n", ColorDim, strings.Repeat("─", 50), ColorReset)
		fmt.Print("\033[2K\r\n")

		// Fields - each gets exactly 2 lines
		for i, f := range activeFields {
			isSelected := i == current

			// Line 1: Field name and value
			fmt.Print("\033[2K\r")
			prefix := "  "
			if isSelected {
				prefix = fmt.Sprintf("%s❯%s ", ColorGreen, ColorReset)
			}

			valueDisplay := f.Value
			if valueDisplay == "" {
				valueDisplay = fmt.Sprintf("%s(not set)%s", ColorDim, ColorReset)
			} else if f.Type == FieldTypePassword {
				valueDisplay = "••••••••"
			}

			fmt.Printf("%s%s%s:%s %s\n", prefix, ColorBold, f.Label, ColorReset, valueDisplay)

			// Line 2: Description (only for selected) or blank
			fmt.Print("\033[2K\r")
			if isSelected && f.Description != "" {
				fmt.Printf("    %s%s%s\n", ColorDim, f.Description, ColorReset)
			} else {
				fmt.Print("\n")
			}
		}

		// Footer - 2 lines
		fmt.Print("\033[2K\r\n")
		fmt.Print("\033[2K\r")
		if editing {
			fmt.Printf("  %s[↑/↓] Select  [Enter] Confirm  [Esc] Back%s\n", ColorDim, ColorReset)
		} else {
			fmt.Printf("  %s[↑/↓] Navigate  [Enter] Edit  [Space] Submit  [q] Quit%s\n", ColorDim, ColorReset)
		}
	}

	// Render inline options when editing a select field
	renderEditOptions := func(options []MenuItem, selected int) {
		fmt.Print("\n") // Move to new line
		for i, opt := range options {
			fmt.Print("\033[2K\r")
			if i == selected {
				fmt.Printf("      %s❯%s %s%s%s\n", ColorGreen, ColorReset, ColorBold, opt.Label, ColorReset)
			} else {
				fmt.Printf("        %s\n", opt.Label)
			}
		}
		fmt.Print("\033[2K\r")
		fmt.Printf("  %s[↑/↓] Select  [Enter] Confirm  [Esc] Back%s", ColorDim, ColorReset)
	}

	clearEditOptions := func(numOptions int) {
		// Move up and clear the option lines
		fmt.Printf("\033[%dA", numOptions+1)
		for i := 0; i < numOptions+1; i++ {
			fmt.Print("\033[2K\r\n")
		}
		fmt.Printf("\033[%dA", numOptions+1)
	}

	renderWizard()

	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}

		if editing {
			f := &activeFields[current]
			switch b {
			case 27: // Escape
				next, _ := reader.ReadByte()
				if next == '[' {
					arrow, _ := reader.ReadByte()
					switch arrow {
					case 'A': // Up
						if editSelected > 0 {
							editSelected--
							clearEditOptions(len(f.Options))
							renderEditOptions(f.Options, editSelected)
						}
					case 'B': // Down
						if editSelected < len(f.Options)-1 {
							editSelected++
							clearEditOptions(len(f.Options))
							renderEditOptions(f.Options, editSelected)
						}
					}
					continue
				}
				// Pure Escape - cancel editing
				clearEditOptions(len(f.Options))
				editing = false
				firstRender = true
				renderWizard()
				continue
			case 13: // Enter - confirm selection
				f.Value = f.Options[editSelected].Label
				f.ValueIndex = editSelected
				clearEditOptions(len(f.Options))
				editing = false
				// Move to next field
				if current < len(activeFields)-1 {
					current++
				}
				firstRender = true
				renderWizard()
			case 'k': // vim up
				if editSelected > 0 {
					editSelected--
					clearEditOptions(len(f.Options))
					renderEditOptions(f.Options, editSelected)
				}
			case 'j': // vim down
				if editSelected < len(f.Options)-1 {
					editSelected++
					clearEditOptions(len(f.Options))
					renderEditOptions(f.Options, editSelected)
				}
			}
		} else {
			// Handle navigation mode
			switch b {
			case 'q': // Quit
				fmt.Print("\n")
				return nil, fmt.Errorf("cancelled")
			case 27: // Escape or arrow keys
				next, _ := reader.ReadByte()
				if next == '[' {
					arrow, _ := reader.ReadByte()
					switch arrow {
					case 'A': // Up
						if current > 0 {
							current--
							renderWizard()
						}
					case 'B': // Down
						if current < len(activeFields)-1 {
							current++
							renderWizard()
						}
					}
					continue
				}
				// Pure Escape - cancel
				fmt.Print("\n")
				return nil, fmt.Errorf("cancelled")
			case 'k': // vim up
				if current > 0 {
					current--
					renderWizard()
				}
			case 'j': // vim down
				if current < len(activeFields)-1 {
					current++
					renderWizard()
				}
			case 9: // Tab - next field
				if current < len(activeFields)-1 {
					current++
					renderWizard()
				}
			case 13: // Enter - edit field
				f := &activeFields[current]
				switch f.Type {
				case FieldTypeSelect, FieldTypeYesNo:
					editing = true
					editSelected = f.ValueIndex
					renderEditOptions(f.Options, editSelected)
				case FieldTypeText, FieldTypePassword:
					// Switch to text input mode
					fmt.Print("\033[?25h") // Show cursor
					term.Restore(int(os.Stdin.Fd()), oldState)

					prompt := fmt.Sprintf("\n  %s: ", f.Label)
					var val string
					if f.Type == FieldTypePassword {
						fmt.Print(prompt)
						password, _ := term.ReadPassword(int(os.Stdin.Fd()))
						val = strings.TrimSpace(string(password))
						fmt.Println()
					} else {
						fmt.Print(prompt)
						lineReader := bufio.NewReader(os.Stdin)
						val, _ = lineReader.ReadString('\n')
						val = strings.TrimSpace(val)
					}
					f.Value = val

					// Re-enter raw mode
					oldState, _ = term.MakeRaw(int(os.Stdin.Fd()))
					fmt.Print("\033[?25l") // Hide cursor
					if current < len(activeFields)-1 {
						current++
					}
					firstRender = true
					renderWizard()
				}
			case ' ': // Space - submit form
				// Build result
				result := &WizardResult{
					Values: make(map[string]interface{}),
				}
				for _, f := range activeFields {
					switch f.Type {
					case FieldTypeYesNo:
						result.Values[f.ID] = f.Value == "Yes"
					case FieldTypeSelect:
						result.Values[f.ID] = f.ValueIndex
						result.Values[f.ID+"_value"] = f.Value
					default:
						result.Values[f.ID] = f.Value
					}
				}
				fmt.Print("\n")
				return result, nil
			}
		}
	}
}

// runWizardFallback handles non-TTY environments.
func runWizardFallback(title string, fields []WizardField) (*WizardResult, error) {
	fmt.Printf("\n%s%s%s%s\n", ColorBold, ColorCyan, title, ColorReset)
	fmt.Printf("%s%s%s\n\n", ColorDim, strings.Repeat("─", 50), ColorReset)

	result := &WizardResult{
		Values: make(map[string]interface{}),
	}

	reader := bufio.NewReader(os.Stdin)
	for _, f := range fields {
		fmt.Printf("%s%s:%s ", ColorBold, f.Label, ColorReset)
		if f.Description != "" {
			fmt.Printf("%s(%s)%s ", ColorDim, f.Description, ColorReset)
		}

		switch f.Type {
		case FieldTypeYesNo:
			fmt.Print("[y/n]: ")
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			result.Values[f.ID] = input == "y" || input == "yes"
		case FieldTypeSelect:
			fmt.Println()
			for i, opt := range f.Options {
				fmt.Printf("  [%d] %s\n", i+1, opt.Label)
			}
			fmt.Print("Enter number: ")
			input, _ := reader.ReadString('\n')
			var num int
			_, _ = fmt.Sscanf(strings.TrimSpace(input), "%d", &num)
			if num >= 1 && num <= len(f.Options) {
				result.Values[f.ID] = num - 1
				result.Values[f.ID+"_value"] = f.Options[num-1].Label
			} else {
				result.Values[f.ID] = 0
			}
		case FieldTypePassword:
			if term.IsTerminal(int(os.Stdin.Fd())) {
				password, _ := term.ReadPassword(int(os.Stdin.Fd()))
				result.Values[f.ID] = strings.TrimSpace(string(password))
				fmt.Println()
			} else {
				input, _ := reader.ReadString('\n')
				result.Values[f.ID] = strings.TrimSpace(input)
			}
		default:
			input, _ := reader.ReadString('\n')
			result.Values[f.ID] = strings.TrimSpace(input)
		}
	}

	return result, nil
}
