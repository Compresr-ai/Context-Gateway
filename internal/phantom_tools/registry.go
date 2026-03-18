package phantom_tools

import (
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
)

// registry is the global phantom tool registry.
// Tools register themselves via init() in their definition files.
var registry = &Registry{
	tools: make(map[string]*PhantomTool),
	stubs: &StubBuilder{},
}

// Registry holds all registered phantom tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*PhantomTool
	order []string // insertion order for deterministic iteration
	stubs *StubBuilder
}

// Register adds a phantom tool to the global registry.
// Typically called from init() in each tool's definition file.
func Register(tool PhantomTool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	if _, exists := registry.tools[tool.Name]; !exists {
		registry.order = append(registry.order, tool.Name)
	}
	registry.tools[tool.Name] = &tool
}

// GetAll returns all registered phantom tools in registration order.
func GetAll() []*PhantomTool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	result := make([]*PhantomTool, 0, len(registry.order))
	for _, name := range registry.order {
		result = append(result, registry.tools[name])
	}
	return result
}

// GetByName returns a specific phantom tool by name, or nil if not found.
func GetByName(name string) *PhantomTool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.tools[name]
}

// AllNames returns the names of all registered phantom tools.
func AllNames() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	names := make([]string, len(registry.order))
	copy(names, registry.order)
	return names
}

// DetectFormat determines the provider format from the provider and request body.
func DetectFormat(body []byte, provider adapters.Provider) ProviderFormat {
	if provider == adapters.ProviderGemini {
		return FormatGemini
	}
	if provider == adapters.ProviderOpenAI || provider == adapters.ProviderOllama || provider == adapters.ProviderLiteLLM || provider == adapters.ProviderMiniMax {
		// Responses API: has "input" but no "messages"
		hasInput := gjson.GetBytes(body, "input").Exists()
		hasMessages := gjson.GetBytes(body, "messages").Exists()
		if hasInput && !hasMessages {
			return FormatOpenAIResponses
		}
		return FormatOpenAIChat
	}
	// Anthropic, Bedrock, unknown -> Anthropic format
	return FormatAnthropic
}

// InjectAll injects all registered phantom tools into the request body.
// Uses sjson append with dedup checking to preserve KV-cache prefix.
// Returns the modified body and any error.
func InjectAll(body []byte, provider adapters.Provider) ([]byte, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	format := DetectFormat(body, provider)
	var err error

	for _, name := range registry.order {
		tool := registry.tools[name]
		toolJSON := tool.GetJSON(format)
		if toolJSON == nil {
			continue
		}

		body, err = InjectPhantomTool(body, tool.Name, toolJSON)
		if err != nil {
			return body, err
		}
	}

	return body, nil
}

// InjectByName injects a specific phantom tool by name into the request body.
// Returns the original body unchanged if the tool is not found.
func InjectByName(body []byte, name string, provider adapters.Provider) ([]byte, error) {
	registry.mu.RLock()
	tool, exists := registry.tools[name]
	registry.mu.RUnlock()

	if !exists {
		return body, nil
	}

	format := DetectFormat(body, provider)
	toolJSON := tool.GetJSON(format)
	if toolJSON == nil {
		return body, nil
	}

	return InjectPhantomTool(body, tool.Name, toolJSON)
}

// BuildStub generates a minimal tool stub for the given tool name and provider.
// Used when phantom tool calls appear in history and the tool must be in tools[].
func BuildStub(toolName string, provider adapters.Provider, body []byte) []byte {
	format := DetectFormat(body, provider)
	return registry.stubs.BuildStub(toolName, format)
}

// InjectStub injects a minimal stub for the given tool name if it's not already present.
func InjectStub(body []byte, toolName string, provider adapters.Provider) ([]byte, error) {
	if HasToolByName(body, toolName) {
		return body, nil
	}

	stub := BuildStub(toolName, provider, body)
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return sjson.SetRawBytes(body, "tools", append(append([]byte{'['}, stub...), ']'))
	}
	return sjson.SetRawBytes(body, "tools.-1", stub)
}
