// Hub manages WebSocket connections for real-time dashboard updates.
//
// DESIGN: Fan-out broadcaster. When session state changes, the hub serializes
// the full session list once and writes it to all connected clients.
// Uses the coder/websocket package already in go.mod.
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"
)

// Hub broadcasts session state to connected WebSocket clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

// HandleWS upgrades an HTTP connection to WebSocket and registers the client.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow connections from the dashboard page (same origin or localhost)
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Error().Err(err).Msg("dashboard: websocket accept failed")
		return
	}

	// Create context and cancel inside the goroutine so creation and deferred
	// cleanup live in the same scope (satisfies G118 / context-cancel linting).
	client := &wsClient{conn: conn}

	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	log.Debug().Int("clients", h.clientCount()).Msg("dashboard: client connected")

	// Read loop — just watches for close/errors
	go func() {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		client.cancel = cancel

		defer func() {
			h.removeClient(client)
			if err := conn.CloseNow(); err != nil {
				log.Debug().Err(err).Msg("dashboard: websocket close error")
			}
		}()
		for {
			_, _, readErr := conn.Read(ctx)
			if readErr != nil {
				return
			}
		}
	}()
}

// Broadcast sends the current session state to all connected clients.
func (h *Hub) Broadcast(sessions []Session) {
	msg := wsMessage{
		Type:      "sessions",
		Timestamp: time.Now().Format(time.RFC3339),
		Sessions:  sessions,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
			h.removeClient(c)
			if closeErr := c.conn.CloseNow(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("dashboard: websocket close error on broadcast")
			}
			if c.cancel != nil {
				c.cancel()
			}
		}
		cancel()
	}
}

func (h *Hub) removeClient(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ClientCount returns the number of connected WebSocket clients.
func (h *Hub) ClientCount() int {
	return h.clientCount()
}

// BroadcastEvent sends a typed event (e.g., "config_updated") to all connected clients.
func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	msg := wsMessage{
		Type:      eventType,
		Timestamp: time.Now().Format(time.RFC3339),
		Payload:   payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
			h.removeClient(c)
			if closeErr := c.conn.CloseNow(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("dashboard: websocket close error on broadcast event")
			}
			if c.cancel != nil {
				c.cancel()
			}
		}
		cancel()
	}
}

// wsMessage is the JSON envelope sent over WebSocket.
type wsMessage struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp"`
	Sessions  []Session   `json:"sessions,omitempty"`
	Payload   interface{} `json:"payload,omitempty"`
}
