package unit

import (
	"io"
	"os"
	"testing"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestMain(m *testing.M) {
	godotenv.Load("../../../.env")
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
	os.Exit(m.Run())
}
