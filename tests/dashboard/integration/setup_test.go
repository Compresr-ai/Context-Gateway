// Dashboard Integration Tests - Setup
//
// These tests use httptest.NewServer with the gateway's HTTP handler to
// verify the health, stats, and savings endpoints return expected responses.
// No real LLM backends needed.
//
// Run with: go test ./tests/dashboard/integration/... -v
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
