// Package auth provides provider-specific authentication handling for the gateway.
//
// Each provider (Anthropic, OpenAI) has different OAuth flows and error handling:
//   - Anthropic: Gateway actively manages tokens (refresh, persist to Keychain)
//   - OpenAI: Passthrough mode - CLI handles OAuth, gateway does fallback on 401
package auth

import (
	"github.com/compresr/context-gateway/internal/auth/types"
)

// Re-export types for convenience
type (
	AuthMode       = types.AuthMode
	AuthConfig     = types.AuthConfig
	FallbackResult = types.FallbackResult
	Handler        = types.Handler
)

// Re-export constants
const (
	AuthModeSubscription = types.AuthModeSubscription
	AuthModeAPIKey       = types.AuthModeAPIKey
	AuthModeBoth         = types.AuthModeBoth

	HeaderAuthorization = types.HeaderAuthorization
	HeaderXAPIKey       = types.HeaderXAPIKey
	HeaderContentType   = types.HeaderContentType
)

// Re-export functions
var (
	ParseAuthMode = types.ParseAuthMode
	BearerToken   = types.BearerToken
)
