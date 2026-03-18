package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
)

// runConfigMigrate handles `context-gateway config migrate`.
// It scans a config file for literal API keys and replaces them with env var references,
// optionally saving the literal values to ~/.config/context-gateway/.env.
func runConfigMigrate(args []string) {
	fs := flag.NewFlagSet("config migrate", flag.ExitOnError)
	configName := fs.String("config", "", "config name or path to migrate (default: fast_setup)")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	_ = fs.Parse(args)

	loadEnvFiles()

	// Resolve config — use the same resolution logic as all other commands.
	// We need the raw (unexpanded) bytes so we can detect literal key values.
	name := *configName
	if name == "" {
		name = "fast_setup"
	}
	rawData, configPath, err := resolveConfig(name)
	if err != nil {
		printError("No config file found. Use --config <name> to specify one.")
		return
	}
	// Embedded configs live in memory and have no writable on-disk path.
	if strings.HasPrefix(configPath, "(embedded)") {
		printError("Cannot migrate an embedded config. Start the gateway once to materialize it, then retry.")
		return
	}

	fmt.Printf("\n%s Context Gateway Config Migration%s\n", tui.ColorCyan, tui.ColorReset)
	fmt.Printf("  Config: %s\n\n", configPath)

	// Parse YAML without env expansion to see literal key values.
	type literalKey struct {
		name   string // field path (e.g., "providers.anthropic.api_key")
		value  string // the literal key value
		envVar string // suggested env var name
	}

	var found []literalKey

	var raw map[string]interface{}
	if err := yaml.Unmarshal(rawData, &raw); err != nil {
		printError(fmt.Sprintf("Failed to parse config YAML: %v", err))
		return
	}

	// Scan providers section
	if providers, ok := raw["providers"].(map[string]interface{}); ok {
		for provName, pData := range providers {
			pd, ok := pData.(map[string]interface{})
			if !ok {
				continue
			}
			auth, _ := pd["auth"].(string)
			if auth == "oauth" || auth == "bedrock" {
				continue
			}
			if apiKey, ok := pd["api_key"].(string); ok {
				if apiKey != "" && !strings.Contains(apiKey, "${") {
					found = append(found, literalKey{
						name:   fmt.Sprintf("providers.%s.api_key", provName),
						value:  apiKey,
						envVar: config.ProviderEnvVar(provName),
					})
				}
			}
		}
	}

	// Scan top-level compresr section
	if compresrSection, ok := raw["compresr"].(map[string]interface{}); ok {
		if apiKey, ok := compresrSection["api_key"].(string); ok {
			if apiKey != "" && !strings.Contains(apiKey, "${") {
				found = append(found, literalKey{
					name:   "compresr.api_key",
					value:  apiKey,
					envVar: "COMPRESR_API_KEY",
				})
			}
		}
	}

	if len(found) == 0 {
		fmt.Printf("%s✓%s No literal API keys found — config already uses env var references.\n",
			tui.ColorGreen, tui.ColorReset)
		return
	}

	fmt.Printf("Found %d literal API key(s):\n\n", len(found))
	for _, k := range found {
		fmt.Printf("  %s%s%s = %s\n    → Suggested: ${%s:-}\n\n",
			tui.ColorYellow, k.name, tui.ColorReset, utils.MaskKeyShort(k.value), k.envVar)
	}

	if *dryRun {
		fmt.Printf("%s(dry-run)%s No changes written.\n", tui.ColorYellow, tui.ColorReset)
		return
	}

	// Interactively replace each key
	scanner := bufio.NewScanner(os.Stdin)
	migratedData := string(rawData)

	for _, k := range found {
		fmt.Printf("Replace %s (%s) with ${%s:-}? [y/N] ",
			k.name, utils.MaskKeyShort(k.value), k.envVar)

		if !scanner.Scan() {
			break
		}
		if !isYes(scanner.Text()) {
			fmt.Printf("  Skipped.\n")
			continue
		}

		// Replace quoted variants only — unquoted api_key values would be malformed YAML.
		newVal := fmt.Sprintf(`api_key: "${%s:-}"`, k.envVar)
		quoted := regexp.MustCompile(`api_key:\s+"` + regexp.QuoteMeta(k.value) + `"`)
		migratedData = quoted.ReplaceAllString(migratedData, newVal)
		singleQuoted := regexp.MustCompile(`api_key:\s+'` + regexp.QuoteMeta(k.value) + `'`)
		migratedData = singleQuoted.ReplaceAllString(migratedData, newVal)

		fmt.Printf("  %s✓%s Replaced with ${%s:-}\n", tui.ColorGreen, tui.ColorReset, k.envVar)

		// Offer to persist the literal value to the global .env file.
		// Uses persistCredential which deduplicates existing keys in the file.
		fmt.Printf("  Save to ~/.config/context-gateway/.env as %s? [y/N] ", k.envVar)
		if !scanner.Scan() {
			continue
		}
		if isYes(scanner.Text()) {
			persistCredential(k.envVar, k.value, ScopeGlobal)
			fmt.Printf("  %s✓%s Saved to .env\n", tui.ColorGreen, tui.ColorReset)
		}
	}

	// Write migrated config
	if migratedData != string(rawData) {
		if err := os.WriteFile(configPath, []byte(migratedData), 0600); err != nil { // #nosec G306 -- config file
			printError(fmt.Sprintf("Failed to write migrated config: %v", err))
			return
		}
		fmt.Printf("\n%s✓%s Config migrated: %s\n", tui.ColorGreen, tui.ColorReset, configPath)
	}
}

func isYes(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "y" || s == "yes"
}
