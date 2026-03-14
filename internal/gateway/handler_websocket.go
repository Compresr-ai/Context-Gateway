package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"
)

var websocketForwardHeaders = []string{
	"Authorization",
	"x-api-key",
	"x-goog-api-key",
	"api-key",
	"anthropic-version",
	"anthropic-beta",
	"Chatgpt-Account-Id",
	"Originator",
	"Session_id",
	"Version",
	"X-Codex-Turn-Metadata",
	"Origin",
	"User-Agent",
	"Accept-Language",
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func (g *Gateway) handleWebSocketProxy(w http.ResponseWriter, r *http.Request) {
	targetURL, err := g.resolveTargetURL(r)
	if err != nil {
		g.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	wsURL, err := buildWebSocketTargetURL(targetURL, r)
	if err != nil {
		g.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	parsedURL, err := url.Parse(wsURL)
	if err != nil {
		g.writeError(w, "invalid websocket target URL", http.StatusBadRequest)
		return
	}
	if !g.isAllowedHost(parsedURL.Host) {
		g.writeError(w, fmt.Sprintf("target host not allowed: %s", parsedURL.Host), http.StatusBadRequest)
		return
	}

	dialHeaders := make(http.Header)
	for _, headerName := range websocketForwardHeaders {
		for _, value := range r.Header.Values(headerName) {
			dialHeaders.Add(headerName, value)
		}
	}

	subprotocols := splitHeaderTokens(r.Header.Values("Sec-WebSocket-Protocol"))

	dialOptions := &websocket.DialOptions{
		HTTPHeader:   dialHeaders,
		Subprotocols: subprotocols,
	}
	if g.httpClient != nil {
		dialOptions.HTTPClient = g.httpClient
	}

	upstreamConn, resp, err := websocket.Dial(r.Context(), wsURL, dialOptions)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		statusCode := http.StatusBadGateway
		if resp != nil && resp.StatusCode > 0 {
			statusCode = resp.StatusCode
		}
		log.Error().Err(err).Str("target_url", wsURL).Int("status", statusCode).Msg("websocket upstream dial failed")
		g.writeError(w, "upstream websocket connection failed", statusCode)
		return
	}
	defer func() { _ = upstreamConn.CloseNow() }()

	acceptOptions := &websocket.AcceptOptions{InsecureSkipVerify: true}
	if upstreamSubprotocol := upstreamConn.Subprotocol(); upstreamSubprotocol != "" {
		acceptOptions.Subprotocols = []string{upstreamSubprotocol}
	}

	clientConn, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		log.Error().Err(err).Msg("websocket client accept failed")
		return
	}
	defer func() { _ = clientConn.CloseNow() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 2)
	go func() {
		errCh <- relayWebSocketMessages(ctx, upstreamConn, clientConn)
	}()
	go func() {
		errCh <- relayWebSocketMessages(ctx, clientConn, upstreamConn)
	}()

	firstErr := <-errCh
	if firstErr != nil {
		cancel()
	}
	secondErr := <-errCh

	logWebSocketProxyError(firstErr)
	logWebSocketProxyError(secondErr)
}

func buildWebSocketTargetURL(targetURL string, r *http.Request) (string, error) {
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("invalid target URL: %w", err)
	}

	switch parsedURL.Scheme {
	case "https":
		parsedURL.Scheme = "wss"
	case "http":
		parsedURL.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported websocket target scheme: %s", parsedURL.Scheme)
	}

	if parsedURL.RawQuery == "" {
		parsedURL.RawQuery = r.URL.RawQuery
	}

	return parsedURL.String(), nil
}

func splitHeaderTokens(values []string) []string {
	var tokens []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			token := strings.TrimSpace(part)
			if token != "" {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

func relayWebSocketMessages(ctx context.Context, dst, src *websocket.Conn) error {
	for {
		msgType, data, err := src.Read(ctx)
		if err != nil {
			if status := websocket.CloseStatus(err); status != -1 {
				_ = dst.Close(status, "")
				return nil
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			_ = dst.Close(websocket.StatusInternalError, "")
			return err
		}

		if err := dst.Write(ctx, msgType, data); err != nil {
			if status := websocket.CloseStatus(err); status != -1 || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func logWebSocketProxyError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || websocket.CloseStatus(err) != -1 {
		return
	}
	log.Debug().Err(err).Msg("websocket proxy loop terminated")
}
