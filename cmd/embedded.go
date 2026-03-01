package main

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed configs/*.yaml
var configsFS embed.FS

//go:embed agents/*.yaml
var agentsFS embed.FS

//go:embed hooks/*.sh
var hooksFS embed.FS

// getEmbeddedConfig returns the raw bytes of an embedded config file.
// name can be with or without the .yaml extension.
func getEmbeddedConfig(name string) ([]byte, error) {
	if !strings.HasSuffix(name, ".yaml") {
		name += ".yaml"
	}
	return configsFS.ReadFile(filepath.Join("configs", name))
}

// getEmbeddedAgent returns the raw bytes of an embedded agent file.
// name can be with or without the .yaml extension.
func getEmbeddedAgent(name string) ([]byte, error) {
	if !strings.HasSuffix(name, ".yaml") {
		name += ".yaml"
	}
	return agentsFS.ReadFile(filepath.Join("agents", name))
}

// getEmbeddedHook returns the raw bytes of an embedded hook script.
// name can be with or without the .sh extension.
func getEmbeddedHook(name string) ([]byte, error) {
	if !strings.HasSuffix(name, ".sh") {
		name += ".sh"
	}
	return hooksFS.ReadFile(filepath.Join("hooks", name))
}

// listEmbeddedConfigs returns the names of all embedded config files (without extension).
func listEmbeddedConfigs() ([]string, error) {
	entries, err := configsFS.ReadDir("configs")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded configs: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// listEmbeddedAgents returns the names of all embedded agent files (without extension).
func listEmbeddedAgents() ([]string, error) {
	entries, err := agentsFS.ReadDir("agents")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded agents: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return names, nil
}
