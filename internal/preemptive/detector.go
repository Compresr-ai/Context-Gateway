// Compaction request detection.
//
// DESIGN: Multi-layer detection for identifying compaction requests:
//
//	Layer 0 (Header):  GenericDetector checks X-Request-Compaction header
//	Layer 1 (Path):    OpenAIDetector checks /compact URL path (Codex)
//	Layer 2 (Prompt):  Pattern matching for Claude Code, Codex, OpenClaw
//
// Supported Agents:
//   - Claude Code: Prompt pattern detection (Anthropic)
//   - Codex: URL path /responses/compact + prompt patterns (OpenAI)
//   - OpenClaw: Header detection + prompt patterns (fallback)
//
// Usage:
//
//	detector := GetDetector(adapters.ProviderAnthropic, config)
//	result := detector.Detect(body)
package preemptive

import (
	"encoding/json"
	"strings"

	"github.com/compresr/context-gateway/internal/adapters"
)

// =============================================================================
// DETECTOR INTERFACE
// =============================================================================

// CompactionDetector is the interface for provider-specific compaction detection.
type CompactionDetector interface {
	Detect(body []byte) DetectionResult
}

// =============================================================================
// DETECTOR FACTORY
// =============================================================================

// GetDetector returns the appropriate detector for the given provider.
func GetDetector(provider adapters.Provider, cfg DetectorsConfig) CompactionDetector {
	switch provider {
	case adapters.ProviderAnthropic:
		return &ClaudeDetector{patterns: cfg.ClaudeCode.PromptPatterns}
	case adapters.ProviderOpenAI:
		return &OpenAIDetector{patterns: cfg.Codex.PromptPatterns}
	default:
		return &ClaudeDetector{patterns: cfg.ClaudeCode.PromptPatterns}
	}
}

// GetGenericDetector returns the generic header-based detector.
func GetGenericDetector(cfg DetectorsConfig) *GenericDetector {
	if !cfg.Generic.Enabled {
		return nil
	}
	return &GenericDetector{
		headerName:  cfg.Generic.HeaderName,
		headerValue: cfg.Generic.HeaderValue,
	}
}

// =============================================================================
// GENERIC DETECTOR (Header-based)
// =============================================================================

// GenericDetector detects compaction requests via HTTP header.
// This is the primary detection method for agents like OpenClaw that don't
// use specific prompt patterns but can send a header to signal compaction.
type GenericDetector struct {
	headerName  string
	headerValue string
}

// DetectFromHeaders checks if the request has the compaction header.
func (d *GenericDetector) DetectFromHeaders(headerValue string) DetectionResult {
	if headerValue == d.headerValue {
		return DetectionResult{
			IsCompactionRequest: true,
			DetectedBy:          "generic_header",
			Confidence:          1.0,
			Details:             map[string]interface{}{"header": d.headerName},
		}
	}
	return DetectionResult{}
}

// HeaderName returns the header name to check.
func (d *GenericDetector) HeaderName() string {
	return d.headerName
}

// =============================================================================
// OPENAI DETECTOR (Codex, GPT, etc.)
// =============================================================================

// OpenAIDetector detects OpenAI-based compaction requests.
type OpenAIDetector struct {
	patterns []string
}

func (d *OpenAIDetector) Detect(body []byte) DetectionResult {
	return d.DetectWithPath(body, "")
}

// DetectWithPath detects compaction requests, checking URL path first (for Codex).
func (d *OpenAIDetector) DetectWithPath(body []byte, path string) DetectionResult {
	// Priority 1: URL path-based detection (Codex sends to /responses/compact)
	if strings.HasSuffix(path, "/compact") {
		return DetectionResult{
			IsCompactionRequest: true,
			DetectedBy:          "openai_path",
			Confidence:          1.0,
			Details:             map[string]interface{}{"path": path},
		}
	}

	// Priority 2: Prompt pattern-based detection
	var req requestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return DetectionResult{}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			text := strings.ToLower(ExtractText(req.Messages[i].Content))
			for _, phrase := range d.patterns {
				if strings.Contains(text, strings.ToLower(phrase)) {
					return DetectionResult{
						IsCompactionRequest: true,
						DetectedBy:          "openai_prompt",
						Confidence:          0.95,
						Details:             map[string]interface{}{"matched_phrase": phrase},
					}
				}
			}
			break
		}
	}
	return DetectionResult{}
}

// =============================================================================
// CLAUDE DETECTOR (Anthropic)
// =============================================================================

// ClaudeDetector detects Claude Code compaction requests.
type ClaudeDetector struct {
	patterns []string
}

func (d *ClaudeDetector) Detect(body []byte) DetectionResult {
	var req requestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return DetectionResult{}
	}

	// Check last user message
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			text := strings.ToLower(ExtractText(req.Messages[i].Content))
			for _, phrase := range d.patterns {
				if strings.Contains(text, strings.ToLower(phrase)) {
					return DetectionResult{
						IsCompactionRequest: true,
						DetectedBy:          "claude_code_prompt",
						Confidence:          0.95,
						Details:             map[string]interface{}{"matched_phrase": phrase},
					}
				}
			}
			break
		}
	}

	return DetectionResult{}
}

// =============================================================================
// SHARED TYPES
// =============================================================================

type requestBody struct {
	Messages []struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	} `json:"messages"`
}
