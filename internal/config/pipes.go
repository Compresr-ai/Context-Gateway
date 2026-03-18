// Pipes configuration re-exports.
package config

import "github.com/compresr/context-gateway/internal/pipes"

// RE-EXPORTS FROM pipes PACKAGE

// Compression ratio constants - re-exported from pipes package.
const (
	DefaultTargetCompressionRatio = pipes.DefaultTargetCompressionRatio
	MinTargetCompressionRatio     = pipes.MinTargetCompressionRatio
	MaxTargetCompressionRatio     = pipes.MaxTargetCompressionRatio
)

// Strategy constants - re-exported from pipes package.
const (
	StrategyPassthrough      = pipes.StrategyPassthrough
	StrategyExternalProvider = pipes.StrategyExternalProvider
	StrategyRelevance        = pipes.StrategyRelevance
	StrategyToolSearch       = pipes.StrategyToolSearch

	// Tool output specific strategies
	StrategyCompresr = pipes.StrategyCompresr
	StrategySimple   = pipes.StrategySimple
	StrategyTrimming = pipes.StrategyTrimming
)

// TYPE ALIASES FOR YAML UNMARSHALING

// PipesConfig is an alias for pipes.Config for use in main Config struct.
type PipesConfig = pipes.Config

// ToolOutputPipeConfig is an alias for pipes.ToolOutputConfig.
type ToolOutputPipeConfig = pipes.ToolOutputConfig

// ToolDiscoveryPipeConfig is an alias for pipes.ToolDiscoveryConfig.
type ToolDiscoveryPipeConfig = pipes.ToolDiscoveryConfig

// CompresrConfig is an alias for pipes.CompresrConfig.
type CompresrConfig = pipes.CompresrConfig
