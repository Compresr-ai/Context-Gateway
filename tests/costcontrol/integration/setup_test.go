// Cost Control Integration Tests - Setup
//
// These tests verify the cost control tracker's accumulation, budget
// enforcement, and session isolation using real CostControlConfig and
// Tracker instances. No mock LLM backends needed.
//
// Run with: go test ./tests/costcontrol/integration/... -v
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
