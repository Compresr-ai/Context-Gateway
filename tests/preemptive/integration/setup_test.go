// Preemptive Integration Tests - Setup
//
// These tests exercise the preemptive summarization subsystem:
// session tracking, token estimation, and trigger threshold logic.
// No real LLM calls — tests the manager and session logic directly.
//
// Run with: go test ./tests/preemptive/integration/... -v
package integration

import (
	"io"
	"os"
	"testing"

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
	os.Exit(m.Run())
}
