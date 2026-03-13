// Monitoring Integration Tests - Setup
//
// These tests exercise the monitoring subsystem: telemetry recording,
// savings tracking, and report generation. Uses httptest.NewServer
// for gateway-level tests.
//
// Run with: go test ./tests/monitoring/integration/... -v
package integration

import (
	"io"
	"os"
	"testing"

	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

func TestMain(m *testing.M) {
	godotenv.Load("../../../.env")
	gateway.EnableLocalHostsForTesting()
	os.Exit(m.Run())
}
