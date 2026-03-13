package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/stretchr/testify/assert"
)

func TestUpdater_DisabledConfig(t *testing.T) {
	cfg := postsession.Config{Enabled: false}
	updater := postsession.NewUpdater(cfg)

	result, err := updater.Update(nil, nil, "", false, "")
	assert.NoError(t, err)
	assert.False(t, result.Updated)
	assert.Contains(t, result.Description, "disabled")
}

func TestUpdater_NoCollector(t *testing.T) {
	cfg := postsession.Config{Enabled: true}
	updater := postsession.NewUpdater(cfg)

	result, err := updater.Update(nil, nil, "", false, "")
	assert.NoError(t, err)
	assert.False(t, result.Updated)
	assert.Contains(t, result.Description, "no session events")
}

func TestUpdater_EmptyCollector(t *testing.T) {
	cfg := postsession.Config{Enabled: true}
	updater := postsession.NewUpdater(cfg)

	collector := postsession.NewSessionCollector()
	result, err := updater.Update(nil, collector, "", false, "")
	assert.NoError(t, err)
	assert.False(t, result.Updated)
	assert.Contains(t, result.Description, "no session events")
}

func TestDefaultConfig(t *testing.T) {
	cfg := postsession.DefaultConfig()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, "claude-haiku-4-5", cfg.Model)
	assert.Equal(t, 8192, cfg.MaxTokens)
}
