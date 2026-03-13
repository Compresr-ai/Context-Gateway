// Post-Session Integration Tests - Setup
//
// These tests verify the postsession collector's event recording,
// session log building, and cleanup behavior. No real LLM calls needed.
//
// Run with: go test ./tests/postsession/integration/... -v
package integration

import (
	"io"
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
