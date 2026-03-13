// Config Integration Tests
//
// Tests the full config loading pipeline: YAML parsing, env var expansion,
// validation, and default value application.
//
// Run with: go test ./tests/config/integration/... -v
package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_Config_LoadFromYAML creates a temporary YAML config file,
// loads it through config.Load(), and verifies all pipe settings are parsed correctly.
func TestIntegration_Config_LoadFromYAML(t *testing.T) {
	yamlContent := `
server:
  port: 19090
  read_timeout: 15s
  write_timeout: 60s

pipes:
  tool_output:
    enabled: true
    strategy: "simple"
    fallback_strategy: "passthrough"
    min_bytes: 2048
    max_bytes: 65536
    target_compression_ratio: 0.3
    include_expand_hint: true
    enable_expand_context: true
  tool_discovery:
    enabled: true
    strategy: "tool-search"
    fallback_strategy: "passthrough"
    min_tools: 3
    max_tools: 20
    target_ratio: 0.5
    enable_search_fallback: true
    max_search_results: 10

store:
  type: "memory"
  ttl: 2h

monitoring:
  log_level: "info"
  log_format: "json"
  log_output: "stdout"
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test-config.yaml")
	err := os.WriteFile(cfgPath, []byte(yamlContent), 0600)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Server settings
	assert.Equal(t, 19090, cfg.Server.Port)

	// Tool output pipe
	assert.True(t, cfg.Pipes.ToolOutput.Enabled)
	assert.Equal(t, "simple", cfg.Pipes.ToolOutput.Strategy)
	assert.Equal(t, "passthrough", cfg.Pipes.ToolOutput.FallbackStrategy)
	assert.Equal(t, 2048, cfg.Pipes.ToolOutput.MinBytes)
	assert.Equal(t, 65536, cfg.Pipes.ToolOutput.MaxBytes)
	assert.InDelta(t, 0.3, cfg.Pipes.ToolOutput.TargetCompressionRatio, 0.001)
	assert.True(t, cfg.Pipes.ToolOutput.IncludeExpandHint)
	assert.True(t, cfg.Pipes.ToolOutput.EnableExpandContext)

	// Tool discovery pipe
	assert.True(t, cfg.Pipes.ToolDiscovery.Enabled)
	assert.Equal(t, "tool-search", cfg.Pipes.ToolDiscovery.Strategy)
	assert.Equal(t, 3, cfg.Pipes.ToolDiscovery.MinTools)
	assert.Equal(t, 20, cfg.Pipes.ToolDiscovery.MaxTools)
	assert.InDelta(t, 0.5, cfg.Pipes.ToolDiscovery.TargetRatio, 0.001)
	assert.True(t, cfg.Pipes.ToolDiscovery.EnableSearchFallback)
	assert.Equal(t, 10, cfg.Pipes.ToolDiscovery.MaxSearchResults)

	// Store settings
	assert.Equal(t, "memory", cfg.Store.Type)
}

// TestIntegration_Config_EnvVarExpansion sets an environment variable,
// uses ${VAR} syntax in YAML, and verifies the value is expanded correctly.
func TestIntegration_Config_EnvVarExpansion(t *testing.T) {
	// Set test env vars
	t.Setenv("TEST_GATEWAY_PORT", "19999")
	t.Setenv("TEST_API_KEY", "sk-test-expansion-key")

	yamlContent := `
server:
  port: ${TEST_GATEWAY_PORT}
  read_timeout: 30s
  write_timeout: 120s

providers:
  anthropic:
    api_key: "${TEST_API_KEY}"
    model: "claude-haiku-4-5"

pipes:
  tool_output:
    enabled: false
    strategy: "passthrough"
    fallback_strategy: "passthrough"
  tool_discovery:
    enabled: false

store:
  type: "memory"
  ttl: 1h

monitoring:
  log_level: "error"
  log_format: "json"
  log_output: "stdout"
`

	cfg, err := config.LoadFromBytes([]byte(yamlContent))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 19999, cfg.Server.Port)
	assert.Equal(t, "sk-test-expansion-key", cfg.Providers["anthropic"].ProviderAuth)

	// Test default value syntax ${VAR:-default}
	yamlWithDefault := `
server:
  port: ${NONEXISTENT_PORT_VAR:-18080}
  read_timeout: 30s
  write_timeout: 120s

pipes:
  tool_output:
    enabled: false
    strategy: "passthrough"
    fallback_strategy: "passthrough"
  tool_discovery:
    enabled: false

store:
  type: "memory"
  ttl: 1h

monitoring:
  log_level: "error"
  log_format: "json"
  log_output: "stdout"
`

	cfg2, err := config.LoadFromBytes([]byte(yamlWithDefault))
	require.NoError(t, err)
	assert.Equal(t, 18080, cfg2.Server.Port, "should use default value when env var is not set")
}

// TestIntegration_Config_DefaultValues loads a minimal config and verifies
// that important default constants are defined in the config package.
func TestIntegration_Config_DefaultValues(t *testing.T) {
	// Verify default constants are accessible and correct
	assert.Equal(t, 5, config.DefaultMinTools, "DefaultMinTools should be 5")
	assert.Equal(t, 25, config.DefaultMaxTools, "DefaultMaxTools should be 25")
	assert.InDelta(t, 0.8, config.DefaultTargetRatio, 0.001, "DefaultTargetRatio should be 0.8")
	assert.Equal(t, 5, config.DefaultMaxSearchResults, "DefaultMaxSearchResults should be 5")
	assert.Equal(t, 1024, config.DefaultMinBytes, "DefaultMinBytes should be 1024")
	assert.Equal(t, 4, config.TokenEstimateRatio, "TokenEstimateRatio should be 4")

	// Verify a minimal valid config loads and validates
	minimalYAML := `
server:
  port: 18080
  read_timeout: 30s
  write_timeout: 120s

pipes:
  tool_output:
    enabled: false
    strategy: "passthrough"
    fallback_strategy: "passthrough"
  tool_discovery:
    enabled: false

store:
  type: "memory"
  ttl: 1h

monitoring:
  log_level: "error"
  log_format: "json"
  log_output: "stdout"
`

	cfg, err := config.LoadFromBytes([]byte(minimalYAML))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify the minimal config passes validation
	err = cfg.Validate()
	assert.NoError(t, err)
}

// TestIntegration_Config_ValidationErrors verifies that invalid configs
// are properly rejected with meaningful error messages.
func TestIntegration_Config_ValidationErrors(t *testing.T) {
	// Missing server.port
	badYAML := `
server:
  read_timeout: 30s
  write_timeout: 120s
store:
  type: "memory"
  ttl: 1h
`
	_, err := config.LoadFromBytes([]byte(badYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server.port")

	// Invalid port number
	badPortYAML := `
server:
  port: 99999
  read_timeout: 30s
  write_timeout: 120s
store:
  type: "memory"
  ttl: 1h
`
	_, err = config.LoadFromBytes([]byte(badPortYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port")

	// Empty config path
	_, err = config.Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file path is required")
}
