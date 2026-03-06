package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// CallbackResult contains the result of the OAuth callback.
type CallbackResult struct {
	Token string
	Error error
}

// CallbackServer is a temporary HTTP server that receives OAuth callbacks.
// It implements CSRF protection via state parameter validation and runs
// on localhost only for security.
type CallbackServer struct {
	port            int
	state           string
	frontendBaseURL string
	server          *http.Server
	resultChan      chan CallbackResult
	once            sync.Once
	mu              sync.Mutex
	completed       bool
}

// NewCallbackServer creates a new callback server.
// It generates a random CSRF state token and finds an available port
// in the range 9876-9885.
// frontendBaseURL is used for redirecting after success/error (e.g., "https://compresr.ai").
func NewCallbackServer(frontendBaseURL string) (*CallbackServer, error) {
	// Generate random 32-byte state for CSRF protection
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("failed to generate state token: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	// Find available port
	port, err := findAvailablePort(9876, 9885)
	if err != nil {
		return nil, fmt.Errorf("no available ports: %w", err)
	}

	cs := &CallbackServer{
		port:            port,
		state:           state,
		frontendBaseURL: frontendBaseURL,
		resultChan:      make(chan CallbackResult, 1),
		completed:       false,
	}

	return cs, nil
}

// Start starts the callback server.
// Returns the callback URL and state that should be sent to the authorization server.
func (cs *CallbackServer) Start() (callbackURL string, state string, err error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)

	// Bind to localhost only for security
	cs.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", cs.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	// Start server in background
	go func() {
		if err := cs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			cs.sendResult(CallbackResult{Error: fmt.Errorf("server error: %w", err)})
		}
	}()

	callbackURL = fmt.Sprintf("http://localhost:%d/callback", cs.port)
	return callbackURL, cs.state, nil
}

// WaitForCallback waits for the OAuth callback with a timeout.
// Returns the token or an error.
func (cs *CallbackServer) WaitForCallback(timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case result := <-cs.resultChan:
		cs.shutdown()
		if result.Error != nil {
			return "", result.Error
		}
		return result.Token, nil
	case <-ctx.Done():
		cs.shutdown()
		return "", fmt.Errorf("timeout waiting for authorization")
	}
}

// handleCallback handles the OAuth callback request.
func (cs *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Check if already handled (one-time use only)
	cs.mu.Lock()
	if cs.completed {
		cs.mu.Unlock()
		cs.redirectToFrontend(w, r, "/authorize/error", "This authorization link has already been used")
		return
	}
	cs.completed = true
	cs.mu.Unlock()

	// Validate HTTP method
	if r.Method != http.MethodGet {
		cs.sendResult(CallbackResult{Error: fmt.Errorf("invalid method: %s", r.Method)})
		cs.redirectToFrontend(w, r, "/authorize/error", "Invalid request method")
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	receivedState := query.Get("state")
	token := query.Get("token")
	errorParam := query.Get("error")

	// Check for error from authorization server
	if errorParam != "" {
		cs.sendResult(CallbackResult{Error: fmt.Errorf("authorization error: %s", errorParam)})
		cs.redirectToFrontend(w, r, "/authorize/error", errorParam)
		return
	}

	// Validate CSRF state
	if receivedState != cs.state {
		cs.sendResult(CallbackResult{Error: fmt.Errorf("state mismatch (CSRF protection)")})
		cs.redirectToFrontend(w, r, "/authorize/error", "Security validation failed. Please try again.")
		return
	}

	// Validate token is present
	if token == "" {
		cs.sendResult(CallbackResult{Error: fmt.Errorf("no token received")})
		cs.redirectToFrontend(w, r, "/authorize/error", "No authorization token received")
		return
	}

	// Success! Send token to waiting goroutine
	cs.sendResult(CallbackResult{Token: token})
	cs.redirectToFrontend(w, r, "/authorize/success", "")
}

// sendResult sends a result to the result channel (only once).
func (cs *CallbackServer) sendResult(result CallbackResult) {
	cs.once.Do(func() {
		cs.resultChan <- result
	})
}

// shutdown gracefully shuts down the server.
func (cs *CallbackServer) shutdown() {
	if cs.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cs.server.Shutdown(ctx)
	}
}

// redirectToFrontend redirects the browser to the frontend success or error page.
// path should be "/authorize/success" or "/authorize/error".
// message is optional and will be included as a query param for error pages.
func (cs *CallbackServer) redirectToFrontend(w http.ResponseWriter, r *http.Request, path string, message string) {
	redirectURL := cs.frontendBaseURL + path
	if message != "" {
		redirectURL += "?message=" + url.QueryEscape(message)
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// findAvailablePort finds an available port in the given range.
func findAvailablePort(start, end int) (int, error) {
	for port := start; port <= end; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", start, end)
}

// GetCallbackURL returns the callback URL with state parameter for the authorization flow.
// This is a convenience method that combines the callback URL and state into a single string
// that can be used directly as a query parameter.
func (cs *CallbackServer) GetCallbackURL() string {
	return fmt.Sprintf("http://localhost:%d/callback", cs.port)
}

// GetState returns the CSRF state token.
func (cs *CallbackServer) GetState() string {
	return cs.state
}

// Close immediately shuts down the server.
func (cs *CallbackServer) Close() error {
	cs.shutdown()
	return nil
}
