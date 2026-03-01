package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// AuthClient connects to the backend via WebSocket to receive OAuth tokens.
// Unlike the old CallbackServer (localhost HTTP), this works from VMs and remote
// machines since it uses an outbound WebSocket connection.
type AuthClient struct {
	backendURL string // e.g. "http://localhost:8000" or "https://compresr.dev"
	state      string // CSRF token
	conn       *websocket.Conn
	mu         sync.Mutex
}

// wsMessage represents a message received over the WebSocket connection.
type wsMessage struct {
	Type         string `json:"type"`
	AuthorizeURL string `json:"authorize_url,omitempty"`
	Token        string `json:"token,omitempty"`
	Error        string `json:"error,omitempty"`
}

// NewAuthClient creates a new auth client that will connect to the backend via WebSocket.
// backendURL is the backend base URL (e.g. "http://localhost:8000" or "https://compresr.dev").
func NewAuthClient(backendURL string) (*AuthClient, error) {
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("failed to generate state token: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	return &AuthClient{
		backendURL: backendURL,
		state:      state,
	}, nil
}

// Connect establishes a WebSocket connection to the backend and waits for the
// authorize URL. Returns the URL that should be opened in the browser.
func (ac *AuthClient) Connect(ctx context.Context) (authorizeURL string, err error) {
	// Build WebSocket URL from the backend URL
	wsURL := toWebSocketURL(ac.backendURL) + "/ws/auth?state=" + ac.state

	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return "", fmt.Errorf("failed to connect to auth server: %w", err)
	}

	ac.mu.Lock()
	ac.conn = conn
	ac.mu.Unlock()

	// Read the first message which should contain the authorize URL
	var msg wsMessage
	if err := ac.readMessage(ctx, &msg); err != nil {
		_ = ac.Close()
		return "", fmt.Errorf("failed to read session message: %w", err)
	}

	if msg.Type == "error" {
		_ = ac.Close()
		return "", fmt.Errorf("auth server error: %s", msg.Error)
	}

	if msg.Type != "session" || msg.AuthorizeURL == "" {
		_ = ac.Close()
		return "", fmt.Errorf("unexpected message type: %s (expected session with authorize_url)", msg.Type)
	}

	return msg.AuthorizeURL, nil
}

// WaitForToken blocks until the backend pushes a token over the WebSocket.
// Must be called after Connect. The context controls the timeout.
func (ac *AuthClient) WaitForToken(ctx context.Context) (string, error) {
	ac.mu.Lock()
	conn := ac.conn
	ac.mu.Unlock()

	if conn == nil {
		return "", fmt.Errorf("not connected (call Connect first)")
	}

	var msg wsMessage
	if err := ac.readMessage(ctx, &msg); err != nil {
		return "", fmt.Errorf("failed waiting for token: %w", err)
	}

	switch msg.Type {
	case "token":
		if msg.Token == "" {
			return "", fmt.Errorf("received empty token from server")
		}
		return msg.Token, nil
	case "error":
		return "", fmt.Errorf("authorization error: %s", msg.Error)
	default:
		return "", fmt.Errorf("unexpected message type: %s (expected token)", msg.Type)
	}
}

// Close closes the WebSocket connection.
func (ac *AuthClient) Close() error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.conn != nil {
		err := ac.conn.Close(websocket.StatusNormalClosure, "done")
		ac.conn = nil
		return err
	}
	return nil
}

// readMessage reads and decodes a JSON message from the WebSocket connection.
func (ac *AuthClient) readMessage(ctx context.Context, msg *wsMessage) error {
	ac.mu.Lock()
	conn := ac.conn
	ac.mu.Unlock()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, msg)
}

// toWebSocketURL converts an HTTP(S) URL to a WS(S) URL.
func toWebSocketURL(httpURL string) string {
	if strings.HasPrefix(httpURL, "https://") {
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	}
	if strings.HasPrefix(httpURL, "http://") {
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	}
	// Already a ws:// or wss:// URL
	return httpURL
}
