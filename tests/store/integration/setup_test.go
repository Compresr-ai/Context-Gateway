// Store Integration Tests - Setup
//
// These tests exercise the MemoryStore with real dual-TTL semantics,
// compressed cache, and expansion records. No external dependencies.
//
// Run with: go test ./tests/store/integration/... -v
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
