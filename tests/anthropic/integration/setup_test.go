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
	// Silence logs during tests
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

func TestMain(m *testing.M) {
	// Load .env from project root
	godotenv.Load("../../../.env")
	// Enable localhost for tests using httptest.NewServer
	gateway.EnableLocalHostsForTesting()
	os.Exit(m.Run())
}
