package main

import (
	"strings"
	"testing"
)

func TestGenerateCustomConfigYAML_UsesGlobalCostCap(t *testing.T) {
	cfg := generateCustomConfigYAML(
		"custom_test",
		"anthropic",
		"claude-haiku-4-5",
		"${ANTHROPIC_API_KEY:-}",
		false,
		85.0,
		12.34,
		false,
		"relevance",
		5,
		25,
		0.8,
		true,
		false,                   // toolOutputEnabled
		"external_provider",     // toolOutputStrategy
		"gemini",                // toolOutputProvider
		"gemini-3-flash",        // toolOutputModel
		"${GEMINI_API_KEY:-}",   // toolOutputAPIKey
	)

	if !strings.Contains(cfg, "cost_control:") {
		t.Fatalf("generated config missing cost_control section")
	}
	if !strings.Contains(cfg, "session_cap: 0") {
		t.Fatalf("expected session_cap to be disabled in generated config")
	}
	if !strings.Contains(cfg, "global_cap: 12.34") {
		t.Fatalf("expected global_cap to be set from spend cap")
	}
}
