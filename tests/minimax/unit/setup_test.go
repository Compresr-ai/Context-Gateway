package unit

import (
	"os"
	"testing"

	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/joho/godotenv"
)

func TestMain(m *testing.M) {
	// Load .env from project root
	godotenv.Load("../../../.env")
	// Enable localhost for tests using httptest.NewServer
	gateway.EnableLocalHostsForTesting()
	os.Exit(m.Run())
}
