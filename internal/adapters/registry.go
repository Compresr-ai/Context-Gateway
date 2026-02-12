// Registry manages adapter registration and lookup.
//
// DESIGN: Thread-safe map of provider name → Adapter.
// Built-in adapters (OpenAI, Anthropic) are registered at startup.
package adapters

import (
	"sync"
)

// Registry manages adapter registration.
type Registry struct {
	adapters map[string]Adapter
	mu       sync.RWMutex
}

// NewRegistry creates a new adapter registry with all built-in adapters.
func NewRegistry() *Registry {
	r := &Registry{
		adapters: make(map[string]Adapter),
	}

	// Register built-in adapters
	r.Register(NewAnthropicAdapter())
	r.Register(NewOpenAIAdapter())
	r.Register(NewBedrockAdapter())

	return r
}

// Register adds an adapter to the registry.
func (r *Registry) Register(adapter Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapter.Name()] = adapter
}

// Get returns an adapter by name.
func (r *Registry) Get(name string) Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[name]
}
