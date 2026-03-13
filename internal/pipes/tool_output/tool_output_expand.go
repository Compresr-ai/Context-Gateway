// Tool output expansion - handles expand_context loop.
//
// V2 DESIGN: When tool outputs are compressed, we inject an expand_context tool.
// If the LLM needs full content, it calls expand_context(shadow_id).
// This file handles that loop:
//  1. Forward request to LLM
//  2. Check response for expand_context tool calls
//  3. If found: retrieve original from store, inject as tool result, repeat
//  4. If not found: filter phantom tool from response, return to client
//
// V2 Improvements:
//   - E10: Circular expansion prevention (track expanded IDs)
//   - E14/E15: Stream buffering for phantom tool suppression
//   - E26: Filter expand_context from final response
//
// MaxExpandLoops (5) prevents infinite recursion.
package tooloutput

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/store"
)

// V2: ExpandContextToolSchema is the JSON schema for the expand_context tool
// Moved to types.go for shared access

// Expander handles the expand_context loop for retrieving full content from shadow refs.
// V2: Tracks expanded IDs to prevent circular expansion (E10).
type Expander struct {
	store       store.Store
	tracker     *monitoring.Tracker
	expandedIDs map[string]bool // V2: Track expanded IDs (E10)
}

// NewExpander creates a new Expander.
func NewExpander(st store.Store, tracker *monitoring.Tracker) *Expander {
	return &Expander{
		store:       st,
		tracker:     tracker,
		expandedIDs: make(map[string]bool),
	}
}

// ExpandResult contains the result of running the expand loop.
type ExpandResult struct {
	ResponseBody        []byte
	Response            *http.Response
	ForwardLatency      time.Duration
	ExpandLoopCount     int
	ExpandCallsFound    int
	ExpandCallsNotFound int
}

// ParseExpandContextCalls extracts expand_context tool calls from an LLM response.
func (e *Expander) ParseExpandContextCalls(responseBody []byte) []ExpandContextCall {
	if len(responseBody) == 0 {
		return nil
	}

	// Check for SSE streaming format
	trimmed := bytes.TrimLeft(responseBody, " \t\n\r")
	if len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}

	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		preview := string(responseBody)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		log.Warn().
			Err(err).
			Str("body_preview", preview).
			Int("body_len", len(responseBody)).
			Msg("expand_context: failed to parse response JSON for expand call detection")
		return nil
	}

	var calls []ExpandContextCall

	// Anthropic format: content array with tool_use blocks
	if content, ok := response["content"].([]interface{}); ok {
		for _, blockInterface := range content {
			block, ok := blockInterface.(map[string]interface{})
			if !ok {
				continue
			}

			if block["type"] != "tool_use" {
				continue
			}

			name, _ := block["name"].(string)
			if name != ExpandContextToolName {
				continue
			}

			toolUseID, _ := block["id"].(string)
			input, _ := block["input"].(map[string]interface{})
			shadowID, _ := input["id"].(string)

			if toolUseID != "" && shadowID != "" {
				calls = append(calls, ExpandContextCall{
					ToolUseID: toolUseID,
					ShadowID:  shadowID,
				})
			}
		}
	}

	// OpenAI Responses API format: output[] with type:"function_call"
	if output, ok := response["output"].([]interface{}); ok {
		for _, itemInterface := range output {
			item, ok := itemInterface.(map[string]interface{})
			if !ok {
				continue
			}

			if item["type"] != "function_call" {
				continue
			}

			name, _ := item["name"].(string)
			if name != ExpandContextToolName {
				continue
			}

			callID, _ := item["call_id"].(string)
			argsStr, _ := item["arguments"].(string)

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				continue
			}

			shadowID, _ := args["id"].(string)
			if callID != "" && shadowID != "" {
				calls = append(calls, ExpandContextCall{
					ToolUseID: callID,
					ShadowID:  shadowID,
				})
			}
		}
	}

	// OpenAI format: choices[].message.tool_calls
	if choices, ok := response["choices"].([]interface{}); ok {
		for _, choiceInterface := range choices {
			choice, ok := choiceInterface.(map[string]interface{})
			if !ok {
				continue
			}

			message, ok := choice["message"].(map[string]interface{})
			if !ok {
				continue
			}

			toolCalls, ok := message["tool_calls"].([]interface{})
			if !ok {
				continue
			}

			for _, tcInterface := range toolCalls {
				tc, ok := tcInterface.(map[string]interface{})
				if !ok {
					continue
				}

				function, ok := tc["function"].(map[string]interface{})
				if !ok {
					continue
				}

				name, _ := function["name"].(string)
				if name != ExpandContextToolName {
					continue
				}

				toolCallID, _ := tc["id"].(string)
				argsStr, _ := function["arguments"].(string)

				var args map[string]interface{}
				if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
					continue
				}

				shadowID, _ := args["id"].(string)
				if toolCallID != "" && shadowID != "" {
					calls = append(calls, ExpandContextCall{
						ToolUseID: toolCallID,
						ShadowID:  shadowID,
					})
				}
			}
		}
	}

	return calls
}

// Pre-computed expand_context tool JSON bytes per provider format.
// Computed once at init time using structs (deterministic field order).
// This ensures byte-identical output across calls, preserving KV-cache stability.
var (
	expandToolJSON_Anthropic  []byte
	expandToolJSON_OpenAIChat []byte
	expandToolJSON_Responses  []byte
)

const expandContextDescription = "IMPORTANT: Tool outputs marked [COMPRESSED] are lossy summaries with tokens removed — they may be missing " +
	"critical details like exact values, line numbers, error messages, or complete file listings. " +
	"Call this tool with the shadow ID (from the <<<SHADOW:shadow_xxx>>> marker) to expand and retrieve the full, " +
	"uncompressed original content. You SHOULD call this when you need precise content for editing, debugging, " +
	"or when the compressed output seems incomplete or truncated."

// marshalNoEscape marshals v to JSON without HTML-escaping < > & characters.
// Go's json.Marshal escapes these by default, which corrupts <<<SHADOW:>>> markers
// that LLMs need to read in tool descriptions.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a trailing newline; strip it
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func init() {
	// Anthropic format: {name, description, input_schema}
	expandToolJSON_Anthropic, _ = marshalNoEscape(struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Type       string `json:"type"`
			Properties struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"input_schema"`
	}{
		Name:        ExpandContextToolName,
		Description: expandContextDescription,
		InputSchema: struct {
			Type       string `json:"type"`
			Properties struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			} `json:"properties"`
			Required []string `json:"required"`
		}{
			Type: "object",
			Properties: struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			}{
				ID: struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				}{Type: "string", Description: "The shadow reference ID to expand (e.g. shadow_abc123)"},
			},
			Required: []string{"id"},
		},
	})

	// OpenAI Chat Completions: {type, function: {name, description, parameters}}
	expandToolJSON_OpenAIChat, _ = marshalNoEscape(struct {
		Type     string `json:"type"`
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  struct {
				Type       string `json:"type"`
				Properties struct {
					ID struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"id"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"parameters"`
		} `json:"function"`
	}{
		Type: "function",
		Function: struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  struct {
				Type       string `json:"type"`
				Properties struct {
					ID struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"id"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"parameters"`
		}{
			Name:        ExpandContextToolName,
			Description: expandContextDescription,
			Parameters: struct {
				Type       string `json:"type"`
				Properties struct {
					ID struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"id"`
				} `json:"properties"`
				Required []string `json:"required"`
			}{
				Type: "object",
				Properties: struct {
					ID struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"id"`
				}{
					ID: struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					}{Type: "string", Description: "The shadow reference ID to expand (e.g. shadow_abc123)"},
				},
				Required: []string{"id"},
			},
		},
	})

	// OpenAI Responses API: {type, name, description, parameters} (flat)
	expandToolJSON_Responses, _ = marshalNoEscape(struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Type       string `json:"type"`
			Properties struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"parameters"`
	}{
		Type:        "function",
		Name:        ExpandContextToolName,
		Description: expandContextDescription,
		Parameters: struct {
			Type       string `json:"type"`
			Properties struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			} `json:"properties"`
			Required []string `json:"required"`
		}{
			Type: "object",
			Properties: struct {
				ID struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"id"`
			}{
				ID: struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				}{Type: "string", Description: "The shadow reference ID to expand (e.g. shadow_abc123)"},
			},
			Required: []string{"id"},
		},
	})
}

// getExpandToolJSON returns the pre-computed expand_context tool bytes for the given provider.
func getExpandToolJSON(provider string, isResponsesAPI bool) []byte {
	if isResponsesAPI {
		return expandToolJSON_Responses
	}
	if provider == "openai" {
		return expandToolJSON_OpenAIChat
	}
	return expandToolJSON_Anthropic
}

// InjectExpandContextTool adds the expand_context tool to a request.
// Always injects when called (no shadowRefs check) to maintain stable tools[] across turns.
// Uses pre-computed JSON bytes + sjson append to preserve KV-cache prefix.
func InjectExpandContextTool(body []byte, shadowRefs map[string]string, provider string) ([]byte, error) {
	// Check if already exists using gjson
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.Exists() {
		alreadyInjected := false
		toolsResult.ForEach(func(_, value gjson.Result) bool {
			if value.Get("name").String() == ExpandContextToolName ||
				value.Get("function.name").String() == ExpandContextToolName {
				alreadyInjected = true
				return false
			}
			return true
		})
		if alreadyInjected {
			return body, nil
		}
	}

	// Detect format
	hasInput := gjson.GetBytes(body, "input").Exists()
	hasMessages := gjson.GetBytes(body, "messages").Exists()
	isResponsesAPI := hasInput && !hasMessages && provider == "openai"

	// Use pre-computed bytes (deterministic, cache-safe)
	toolJSON := getExpandToolJSON(provider, isResponsesAPI)

	// If tools array doesn't exist, create it with just this tool
	if !toolsResult.Exists() {
		return sjson.SetRawBytes(body, "tools", append(append([]byte{'['}, toolJSON...), ']'))
	}

	// Append to existing tools array using sjson "-1" (append) syntax
	return sjson.SetRawBytes(body, "tools.-1", toolJSON)
}

// InvalidateExpandedMappings is intentionally a no-op.
// Previously this deleted compressed cache entries after expansion, but that breaks
// KV-cache prefix matching: the compressed cache must persist so subsequent turns
// re-apply the same compression deterministically.
func (e *Expander) InvalidateExpandedMappings(shadowIDs []string) {
	// No-op: compressed cache entries are preserved for KV-cache determinism.
	// The original content is still available via store.Get() for future expansions.
	for _, id := range shadowIDs {
		log.Debug().Str("shadow_id", id).Msg("skipping compressed mapping invalidation (KV-cache preservation)")
	}
}

// extractExpandPatterns extracts all shadow IDs from <<<EXPAND:shadow_xxx>>> patterns in text.
func extractExpandPatterns(text string) []string {
	var ids []string
	remaining := text
	for {
		idx := strings.Index(remaining, ExpandContextTextPrefix)
		if idx < 0 {
			break
		}
		start := idx + len(ExpandContextTextPrefix)
		end := strings.Index(remaining[start:], ExpandContextTextSuffix)
		if end < 0 {
			break
		}
		id := remaining[start : start+end]
		if id != "" {
			ids = append(ids, id)
		}
		remaining = remaining[start+end+len(ExpandContextTextSuffix):]
	}
	return ids
}

// ParseExpandPatternsFromText scans assistant text content for <<<EXPAND:shadow_xxx>>> patterns.
// Returns a list of shadow IDs found. Works for both Anthropic and OpenAI response formats.
func ParseExpandPatternsFromText(responseBody []byte) []string {
	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil
	}

	var allText []string

	// Anthropic format: content array with text blocks
	if content, ok := response["content"].([]interface{}); ok {
		for _, blockInterface := range content {
			block, ok := blockInterface.(map[string]interface{})
			if !ok {
				continue
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					allText = append(allText, text)
				}
			}
		}
	}

	// OpenAI format: choices[].message.content
	if choices, ok := response["choices"].([]interface{}); ok {
		for _, choiceInterface := range choices {
			choice, ok := choiceInterface.(map[string]interface{})
			if !ok {
				continue
			}
			message, ok := choice["message"].(map[string]interface{})
			if !ok {
				continue
			}
			if content, ok := message["content"].(string); ok {
				allText = append(allText, content)
			}
		}
	}

	// Extract shadow IDs from all text
	var shadowIDs []string
	seen := make(map[string]bool)
	for _, text := range allText {
		ids := extractExpandPatterns(text)
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				shadowIDs = append(shadowIDs, id)
			}
		}
	}

	return shadowIDs
}
