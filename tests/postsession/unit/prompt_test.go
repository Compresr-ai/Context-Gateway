package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/stretchr/testify/assert"
)

func TestBuildUserPrompt_WithExistingClaudeMD(t *testing.T) {
	prompt := postsession.BuildUserPrompt("Session: 5 requests", "# My Project\nExisting content")
	assert.Contains(t, prompt, "Session: 5 requests")
	assert.Contains(t, prompt, "# My Project")
	assert.Contains(t, prompt, "Existing content")
	assert.Contains(t, prompt, "NO_CHANGES_NEEDED")
}

func TestBuildUserPrompt_WithoutClaudeMD(t *testing.T) {
	prompt := postsession.BuildUserPrompt("Session: 5 requests", "")
	assert.Contains(t, prompt, "Session: 5 requests")
	assert.Contains(t, prompt, "No CLAUDE.md exists yet")
}
