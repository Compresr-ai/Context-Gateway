// Pipes configuration re-exports.
//
// DESIGN: Pipe configuration is defined in internal/pipes/config.go.
// This file re-exports those types for use by the main Config struct.
// This keeps pipe configuration close to pipe implementation while allowing
// the config package to use the types without circular imports.
package config

import "github.com/compresr/context-gateway/internal/pipes"

// =============================================================================
// RE-EXPORTS FROM pipes PACKAGE
// =============================================================================

// Strategy constants - re-exported from pipes package.
const (
	StrategyPassthrough      = pipes.StrategyPassthrough
	StrategyAPI              = pipes.StrategyAPI
	StrategyExternalProvider = pipes.StrategyExternalProvider
)

// CompressionThreshold type alias - re-exported from pipes package.
type CompressionThreshold = pipes.CompressionThreshold

// Threshold constants - re-exported from pipes package.
const (
	ThresholdOff  = pipes.ThresholdOff
	Threshold256  = pipes.Threshold256
	Threshold1K   = pipes.Threshold1K
	Threshold2K   = pipes.Threshold2K
	Threshold4K   = pipes.Threshold4K
	Threshold8K   = pipes.Threshold8K
	Threshold16K  = pipes.Threshold16K
	Threshold32K  = pipes.Threshold32K
	Threshold64K  = pipes.Threshold64K
	Threshold128K = pipes.Threshold128K
)

// DefaultThreshold - re-exported from pipes package.
const DefaultThreshold = pipes.DefaultThreshold

// ThresholdTokenCounts - re-exported from pipes package.
var ThresholdTokenCounts = pipes.ThresholdTokenCounts

// ParseCompressionThreshold - re-exported from pipes package.
var ParseCompressionThreshold = pipes.ParseCompressionThreshold

// =============================================================================
// TYPE ALIASES FOR YAML UNMARSHALING
// =============================================================================

// PipesConfig is an alias for pipes.Config for use in main Config struct.
type PipesConfig = pipes.Config

// ToolOutputPipeConfig is an alias for pipes.ToolOutputConfig.
type ToolOutputPipeConfig = pipes.ToolOutputConfig

// ToolDiscoveryPipeConfig is an alias for pipes.ToolDiscoveryConfig.
type ToolDiscoveryPipeConfig = pipes.ToolDiscoveryConfig

// APIConfig is an alias for pipes.APIConfig.
type APIConfig = pipes.APIConfig
