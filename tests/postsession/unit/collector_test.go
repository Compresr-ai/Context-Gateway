package unit

import (
	"strings"
	"testing"

	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCollector_Empty(t *testing.T) {
	c := postsession.NewSessionCollector()
	assert.False(t, c.HasEvents())
	assert.Empty(t, c.BuildSessionLog())
}

func TestSessionCollector_RecordRequest(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 10)
	assert.True(t, c.HasEvents())

	log := c.BuildSessionLog()
	assert.Contains(t, log, "1 requests")
	assert.Contains(t, log, "claude-3-5-sonnet")
}

func TestSessionCollector_RecordMultipleRequests(t *testing.T) {
	c := postsession.NewSessionCollector()
	for i := 0; i < 15; i++ {
		c.RecordRequest("claude-3-5-sonnet", i+1)
	}

	log := c.BuildSessionLog()
	assert.Contains(t, log, "15 requests")
}

func TestSessionCollector_RecordToolCalls(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 5)
	c.RecordToolCalls([]string{"Read", "Write", "Read"})

	log := c.BuildSessionLog()
	assert.Contains(t, log, "Read(2)")
	assert.Contains(t, log, "Write(1)")
}

func TestSessionCollector_RecordCompression(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 5)
	c.RecordCompression("bash_output", 10000, 2000)

	log := c.BuildSessionLog()
	assert.Contains(t, log, "bash_output")
	assert.Contains(t, log, "10000")
}

func TestSessionCollector_RecordCompaction(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 50)
	c.RecordCompaction("claude-3-5-sonnet")

	log := c.BuildSessionLog()
	assert.Contains(t, log, "1 compactions")
	assert.Contains(t, log, "compaction")
}

func TestSessionCollector_CaptureAuth(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.CaptureAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-test123",
		IsXAPIKey: true,
		Endpoint:  "https://api.anthropic.com/v1/messages",
	})

	auth := c.GetAuth()
	assert.Equal(t, "sk-ant-test123", auth.Token)
	assert.True(t, auth.IsXAPIKey)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", auth.Endpoint)
}

func TestSessionCollector_CaptureAuth_SkipsEmpty(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.CaptureAuth(authtypes.CapturedAuth{})

	assert.Empty(t, c.GetAuth().Token)
}

func TestSessionCollector_BuildSessionLog_Format(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 10)
	c.RecordCompression("tool_result", 5000, 1000)
	c.RecordCompaction("claude-3-5-sonnet")

	log := c.BuildSessionLog()
	require.NotEmpty(t, log)

	// Should have structured sections
	assert.True(t, strings.Contains(log, "Session:"))
	assert.True(t, strings.Contains(log, "Models:"))
	assert.True(t, strings.Contains(log, "Timeline:"))
}

func TestSessionCollector_AssistantContent_Truncated(t *testing.T) {
	c := postsession.NewSessionCollector()
	c.RecordRequest("claude-3-5-sonnet", 5)

	longContent := strings.Repeat("a", 1000)
	c.RecordAssistantContent(longContent)

	log := c.BuildSessionLog()
	// Should be truncated to 500 chars + "..."
	assert.Contains(t, log, "...")
}
