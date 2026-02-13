package unit

import (
	"os"
	"testing"

	"github.com/joho/godotenv"
)

func TestMain(m *testing.M) {
	// Load .env from project root
	godotenv.Load("../../../.env")
	os.Exit(m.Run())
}
