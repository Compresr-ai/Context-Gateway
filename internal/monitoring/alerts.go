// Package monitoring - alerts.go flags anomalies and errors.
package monitoring

import "time"

// AlertManager flags anomalies and errors.
type AlertManager struct {
	logger               *Logger
	highLatencyThreshold time.Duration
}

// NewAlertManager creates a new alert manager.
func NewAlertManager(logger *Logger, cfg AlertConfig) *AlertManager {
	threshold := cfg.HighLatencyThreshold
	if threshold == 0 {
		threshold = 5 * time.Second
	}
	return &AlertManager{logger: logger, highLatencyThreshold: threshold}
}

// FlagHighLatency logs when request latency exceeds threshold.
func (am *AlertManager) FlagHighLatency(requestID string, latency time.Duration, provider, path string) {
	if latency < am.highLatencyThreshold {
		return
	}
	am.logger.Warn().
		Str("request_id", requestID).
		Dur("latency", latency).
		Str("provider", provider).
		Msg("high_latency")
}

// FlagProviderError logs upstream provider error.
func (am *AlertManager) FlagProviderError(requestID, provider string, statusCode int, errorMsg string) {
	am.logger.Warn().
		Str("request_id", requestID).
		Str("provider", provider).
		Int("status", statusCode).
		Str("error", errorMsg).
		Msg("provider_error")
}

// FlagInvalidRequest logs invalid request.
func (am *AlertManager) FlagInvalidRequest(requestID, reason string, details map[string]any) {
	am.logger.Debug().
		Str("request_id", requestID).
		Str("reason", reason).
		Fields(details).
		Msg("invalid_request")
}

// FlagPanic logs recovered panic.
func (am *AlertManager) FlagPanic(requestID string, panicValue any, stack string) {
	am.logger.Error().
		Str("request_id", requestID).
		Interface("panic", panicValue).
		Msg("panic_recovered")
}
