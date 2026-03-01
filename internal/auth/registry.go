package auth

import (
	"fmt"
	"sync"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/auth/types"
)

// Registry maps providers to their authentication handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[adapters.Provider]types.Handler
}

// NewRegistry creates a new auth handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[adapters.Provider]types.Handler),
	}
}

// Register adds a handler for a provider.
func (r *Registry) Register(provider adapters.Provider, handler types.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[provider] = handler
}

// Get returns the handler for a provider.
// Returns nil if no handler is registered.
func (r *Registry) Get(provider adapters.Provider) types.Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[provider]
}

// GetOrDefault returns the handler for a provider, or a no-op handler if not found.
func (r *Registry) GetOrDefault(provider adapters.Provider) types.Handler {
	h := r.Get(provider)
	if h == nil {
		return &NoOpHandler{provider: provider.String()}
	}
	return h
}

// Initialize initializes all registered handlers with their configs.
func (r *Registry) Initialize(configs map[adapters.Provider]types.AuthConfig) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for provider, handler := range r.handlers {
		cfg, ok := configs[provider]
		if !ok {
			// Use default config if not specified
			cfg = types.AuthConfig{Mode: types.AuthModeAPIKey}
		}
		if err := handler.Initialize(cfg); err != nil {
			return fmt.Errorf("failed to initialize %s auth handler: %w", provider, err)
		}
	}
	return nil
}

// Stop stops all registered handlers (cleanup).
func (r *Registry) Stop() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, handler := range r.handlers {
		handler.Stop()
	}
}

// Providers returns all registered provider names.
func (r *Registry) Providers() []adapters.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]adapters.Provider, 0, len(r.handlers))
	for p := range r.handlers {
		providers = append(providers, p)
	}
	return providers
}
