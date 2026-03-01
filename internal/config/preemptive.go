// Preemptive summarization configuration re-exports.
//
// DESIGN: Preemptive config is defined in internal/preemptive/types.go.
// This file re-exports those types for use by the main Config struct.
package config

import "github.com/compresr/context-gateway/internal/preemptive"

// CodexDetectorConfig is an alias for preemptive.CodexDetectorConfig.
type CodexDetectorConfig = preemptive.CodexDetectorConfig

// =============================================================================
// RE-EXPORTS FROM preemptive PACKAGE
// =============================================================================

// PreemptiveConfig is an alias for preemptive.Config for use in main Config struct.
type PreemptiveConfig = preemptive.Config

// SummarizerConfig is an alias for preemptive.SummarizerConfig.
type SummarizerConfig = preemptive.SummarizerConfig

// SessionConfig is an alias for preemptive.SessionConfig.
type SessionConfig = preemptive.SessionConfig

// DetectorsConfig is an alias for preemptive.DetectorsConfig.
type DetectorsConfig = preemptive.DetectorsConfig

// ClaudeCodeDetectorConfig is an alias for preemptive.ClaudeCodeDetectorConfig.
type ClaudeCodeDetectorConfig = preemptive.ClaudeCodeDetectorConfig

// GenericDetectorConfig is an alias for preemptive.GenericDetectorConfig.
type GenericDetectorConfig = preemptive.GenericDetectorConfig
