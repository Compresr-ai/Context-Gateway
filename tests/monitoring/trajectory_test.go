package monitoring_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrajectoryRecorder_BasicRecording(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "test-session-123",
		AgentName: "test-agent",
		Version:   "1.0.0",
	})
	require.NoError(t, err)

	err = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Hello, what tools do you have?"},
		monitoring.AgentTurnData{
			Message:          "I have many tools...",
			Model:            "claude-sonnet-4",
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	)
	require.NoError(t, err)

	err = rec.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	assert.Equal(t, "ATIF-v1.6", traj.SchemaVersion)
	assert.Equal(t, "test-session-123", traj.SessionID)
	assert.Equal(t, "test-agent", traj.Agent.Name)
	assert.Len(t, traj.Steps, 2)

	assert.Equal(t, 1, traj.Steps[0].StepID)
	assert.Equal(t, monitoring.StepSourceUser, traj.Steps[0].Source)
	assert.Equal(t, "Hello, what tools do you have?", traj.Steps[0].Message)

	assert.Equal(t, 2, traj.Steps[1].StepID)
	assert.Equal(t, monitoring.StepSourceAgent, traj.Steps[1].Source)
	assert.Equal(t, "I have many tools...", traj.Steps[1].Message)
	assert.Equal(t, "claude-sonnet-4", traj.Steps[1].ModelName)
	require.NotNil(t, traj.Steps[1].Metrics)
	assert.Equal(t, 100, traj.Steps[1].Metrics.PromptTokens)
	assert.Equal(t, 50, traj.Steps[1].Metrics.CompletionTokens)

	require.NotNil(t, traj.FinalMetrics)
	assert.Equal(t, 100, traj.FinalMetrics.TotalPromptTokens)
	assert.Equal(t, 50, traj.FinalMetrics.TotalCompletionTokens)
	assert.Equal(t, 2, traj.FinalMetrics.TotalSteps)
}

func TestTrajectoryRecorder_WithToolCalls(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "tool-test",
	})
	require.NoError(t, err)

	err = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Read the file main.go"},
		monitoring.AgentTurnData{
			Message: "I'll read that file for you.",
			Model:   "claude-sonnet-4",
			ToolCalls: []monitoring.ToolCall{
				{
					ToolCallID:   "call_123",
					FunctionName: "read_file",
					Arguments:    map[string]any{"path": "main.go"},
				},
			},
			Observations: []monitoring.ObservationResult{
				{
					SourceCallID: "call_123",
					Content:      "package main\n\nfunc main() {}",
				},
			},
		},
	)
	require.NoError(t, err)

	err = rec.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	agentStep := traj.Steps[1]
	require.Len(t, agentStep.ToolCalls, 1)
	assert.Equal(t, "call_123", agentStep.ToolCalls[0].ToolCallID)
	assert.Equal(t, "read_file", agentStep.ToolCalls[0].FunctionName)

	require.NotNil(t, agentStep.Observation)
	require.Len(t, agentStep.Observation.Results, 1)
	assert.Equal(t, "call_123", agentStep.Observation.Results[0].SourceCallID)
}

func TestTrajectoryRecorder_AccumulateToolCalls(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "accumulate-test",
	})
	require.NoError(t, err)

	err = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Do multiple things"},
		monitoring.AgentTurnData{
			Message: "I'll do that.",
			Model:   "claude-sonnet-4",
			ToolCalls: []monitoring.ToolCall{
				{ToolCallID: "call_1", FunctionName: "tool_a", Arguments: map[string]any{}},
			},
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	)
	require.NoError(t, err)

	rec.AccumulateToolCalls(
		[]monitoring.ToolCall{
			{ToolCallID: "call_2", FunctionName: "tool_b", Arguments: map[string]any{}},
		},
		[]monitoring.ObservationResult{
			{SourceCallID: "call_1", Content: "result_1"},
			{SourceCallID: "call_2", Content: "result_2"},
		},
		&monitoring.Metrics{PromptTokens: 200, CompletionTokens: 100},
	)

	err = rec.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	assert.Len(t, traj.Steps, 2)

	agentStep := traj.Steps[1]
	assert.Len(t, agentStep.ToolCalls, 2)
	assert.Equal(t, "call_1", agentStep.ToolCalls[0].ToolCallID)
	assert.Equal(t, "call_2", agentStep.ToolCalls[1].ToolCallID)

	require.NotNil(t, agentStep.Observation)
	assert.Len(t, agentStep.Observation.Results, 2)

	require.NotNil(t, agentStep.Metrics)
	assert.Equal(t, 300, agentStep.Metrics.PromptTokens)
	assert.Equal(t, 150, agentStep.Metrics.CompletionTokens)
}

func TestTrajectoryRecorder_Validation_SequentialStepIDs(t *testing.T) {
	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		SessionID: "validation-test",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(monitoring.UserTurnData{Message: "Turn 1"}, monitoring.AgentTurnData{Message: "Response 1"})
	_ = rec.RecordUserTurn(monitoring.UserTurnData{Message: "Turn 2"}, monitoring.AgentTurnData{Message: "Response 2"})

	err = rec.Validate()
	assert.NoError(t, err, "Sequential step IDs should validate")
}

func TestTrajectoryRecorder_Validation_InvalidSourceCallID(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "validation-invalid",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Test"},
		monitoring.AgentTurnData{
			Message: "Response",
			ToolCalls: []monitoring.ToolCall{
				{ToolCallID: "call_real", FunctionName: "func", Arguments: map[string]any{}},
			},
			Observations: []monitoring.ObservationResult{
				{SourceCallID: "call_nonexistent", Content: "oops"},
			},
		},
	)

	err = rec.Validate()
	assert.Error(t, err, "Should fail validation for invalid source_call_id")
	assert.Contains(t, err.Error(), "unknown tool_call_id")
}

func TestTrajectoryRecorder_MultipleTurns(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "multi-turn",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "First question"},
		monitoring.AgentTurnData{Message: "First answer", Model: "model-a"},
	)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Second question"},
		monitoring.AgentTurnData{Message: "Second answer", Model: "model-b"},
	)

	err = rec.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	assert.Len(t, traj.Steps, 4)
	assert.Equal(t, 1, traj.Steps[0].StepID)
	assert.Equal(t, 2, traj.Steps[1].StepID)
	assert.Equal(t, 3, traj.Steps[2].StepID)
	assert.Equal(t, 4, traj.Steps[3].StepID)

	assert.Equal(t, monitoring.StepSourceUser, traj.Steps[0].Source)
	assert.Equal(t, monitoring.StepSourceAgent, traj.Steps[1].Source)
	assert.Equal(t, monitoring.StepSourceUser, traj.Steps[2].Source)
	assert.Equal(t, monitoring.StepSourceAgent, traj.Steps[3].Source)
}

func TestTrajectoryRecorder_HarborCompatibleOutput(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "trajectory.json")

	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: "harbor-compat",
		AgentName: "claude-code",
		Version:   "1.0.0",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Read main.go"},
		monitoring.AgentTurnData{
			Message: "Here's the file content.",
			Model:   "claude-sonnet-4",
			ToolCalls: []monitoring.ToolCall{
				{ToolCallID: "toolu_123", FunctionName: "Read", Arguments: map[string]any{"file": "main.go"}},
			},
			Observations: []monitoring.ObservationResult{
				{SourceCallID: "toolu_123", Content: "package main"},
			},
			PromptTokens:     500,
			CompletionTokens: 100,
			CostUSD:          0.01,
		},
	)

	err = rec.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.Contains(t, raw, "schema_version")
	assert.Contains(t, raw, "session_id")
	assert.Contains(t, raw, "agent")
	assert.Contains(t, raw, "steps")

	assert.Equal(t, "ATIF-v1.6", raw["schema_version"])

	agent, ok := raw["agent"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, agent, "name")
	assert.Contains(t, agent, "version")

	steps, ok := raw["steps"].([]any)
	require.True(t, ok)
	assert.Len(t, steps, 2)

	agentStep, ok := steps[1].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, agentStep, "step_id")
	assert.Contains(t, agentStep, "source")
	assert.Contains(t, agentStep, "message")
	assert.Contains(t, agentStep, "model_name")
	assert.Contains(t, agentStep, "tool_calls")
	assert.Contains(t, agentStep, "observation")
	assert.Contains(t, agentStep, "metrics")

	toolCalls, ok := agentStep["tool_calls"].([]any)
	require.True(t, ok)
	tc, ok := toolCalls[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, tc, "tool_call_id")
	assert.Contains(t, tc, "function_name")
	assert.Contains(t, tc, "arguments")

	obs, ok := agentStep["observation"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, obs, "results")
}

func TestTrajectoryRecorder_Validation_EmptyToolCallID(t *testing.T) {
	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		SessionID: "validation-empty-tcid",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Test"},
		monitoring.AgentTurnData{
			Message: "Response",
			ToolCalls: []monitoring.ToolCall{
				{ToolCallID: "", FunctionName: "some_func", Arguments: map[string]any{}},
			},
		},
	)

	err = rec.Validate()
	assert.Error(t, err, "Should fail validation for empty tool_call_id")
	assert.Contains(t, err.Error(), "tool_call_id cannot be empty")
}

func TestTrajectoryRecorder_Validation_EmptyFunctionName(t *testing.T) {
	rec, err := monitoring.NewTrajectoryRecorder(monitoring.TrajectoryRecorderConfig{
		SessionID: "validation-empty-fn",
	})
	require.NoError(t, err)

	_ = rec.RecordUserTurn(
		monitoring.UserTurnData{Message: "Test"},
		monitoring.AgentTurnData{
			Message: "Response",
			ToolCalls: []monitoring.ToolCall{
				{ToolCallID: "call_123", FunctionName: "", Arguments: map[string]any{}},
			},
		},
	)

	err = rec.Validate()
	assert.Error(t, err, "Should fail validation for empty function_name")
	assert.Contains(t, err.Error(), "function_name cannot be empty")
}

// ============================================================================
// TrajectoryStore Tests - Multi-session management
// ============================================================================

func TestTrajectoryStore_MultiSession(t *testing.T) {
	tmpDir := t.TempDir()

	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled:   true,
		BaseDir:   tmpDir,
		AgentName: "test-agent",
	})

	// Record to session 1
	store.RecordUserMessage("session-1", "Hello from session 1")
	store.RecordAgentResponse("session-1", monitoring.AgentResponseData{
		Message: "Response to session 1",
		Model:   "model-a",
	})

	// Record to session 2
	store.RecordUserMessage("session-2", "Hello from session 2")
	store.RecordAgentResponse("session-2", monitoring.AgentResponseData{
		Message: "Response to session 2",
		Model:   "model-b",
	})

	assert.Equal(t, 2, store.GetSessionCount())

	err := store.Close()
	require.NoError(t, err)

	// Verify both files exist
	_, err = os.Stat(filepath.Join(tmpDir, "trajectory_session-1.json"))
	assert.NoError(t, err, "session-1 trajectory should exist")

	_, err = os.Stat(filepath.Join(tmpDir, "trajectory_session-2.json"))
	assert.NoError(t, err, "session-2 trajectory should exist")
}

func TestTrajectoryStore_Disabled(t *testing.T) {
	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled: false,
		BaseDir: "/tmp/should-not-exist",
	})

	assert.False(t, store.Enabled())

	// Should not panic when disabled
	store.RecordUserMessage("test", "Hello")
	store.RecordAgentResponse("test", monitoring.AgentResponseData{Message: "Hi"})
	store.MarkMainSession("test")
	store.SetAgentModel("test", "model")

	assert.Equal(t, 0, store.GetSessionCount())

	err := store.Close()
	assert.NoError(t, err)
}

func TestTrajectoryStore_MarkMainSession(t *testing.T) {
	tmpDir := t.TempDir()

	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled:   true,
		BaseDir:   tmpDir,
		AgentName: "test-agent",
	})

	store.MarkMainSession("main-session")
	store.MarkMainSession("main-session") // should be idempotent

	assert.True(t, store.IsMainSession("main-session"))
	assert.False(t, store.IsMainSession("other-session"))

	_ = store.Close()
}

func TestTrajectoryStore_ProxyInteraction(t *testing.T) {
	tmpDir := t.TempDir()

	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled:   true,
		BaseDir:   tmpDir,
		AgentName: "test-agent",
	})

	store.RecordUserMessage("test", "Hello")
	store.RecordAgentResponse("test", monitoring.AgentResponseData{
		Message: "Hi there",
	})

	store.RecordProxyInteraction("test", monitoring.ProxyInteractionData{
		PipeType:           "tool_output",
		PipeStrategy:       "llm",
		ClientTokens:       1000,
		CompressedTokens:   500,
		CompressionEnabled: true,
	})

	err := store.Close()
	require.NoError(t, err)

	// Read the file and verify proxy interaction
	data, err := os.ReadFile(filepath.Join(tmpDir, "trajectory_test.json"))
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	agentStep := traj.Steps[1]
	require.NotNil(t, agentStep.ProxyInteraction)
	assert.Equal(t, "tool_output", agentStep.ProxyInteraction.PipeType)
	assert.NotNil(t, agentStep.ProxyInteraction.Compression)
	assert.True(t, agentStep.ProxyInteraction.Compression.Enabled)
}

func TestTrajectoryStore_AccumulateAgentResponse(t *testing.T) {
	tmpDir := t.TempDir()

	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled:   true,
		BaseDir:   tmpDir,
		AgentName: "test-agent",
	})

	store.RecordUserMessage("test", "Do something")
	store.RecordAgentResponse("test", monitoring.AgentResponseData{
		Message: "I'll do that",
		ToolCalls: []monitoring.ToolCall{
			{ToolCallID: "call_1", FunctionName: "tool_a", Arguments: map[string]any{}},
		},
		PromptTokens:     100,
		CompletionTokens: 50,
	})

	// Accumulate more tool calls (simulating tool loop)
	store.AccumulateAgentResponse("test", monitoring.AgentResponseData{
		ToolCalls: []monitoring.ToolCall{
			{ToolCallID: "call_2", FunctionName: "tool_b", Arguments: map[string]any{}},
		},
		PromptTokens:     200,
		CompletionTokens: 100,
	})

	err := store.Close()
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, "trajectory_test.json"))
	require.NoError(t, err)

	var traj monitoring.Trajectory
	err = json.Unmarshal(data, &traj)
	require.NoError(t, err)

	agentStep := traj.Steps[1]
	assert.Len(t, agentStep.ToolCalls, 2)
	require.NotNil(t, agentStep.Metrics)
	assert.Equal(t, 300, agentStep.Metrics.PromptTokens)
	assert.Equal(t, 150, agentStep.Metrics.CompletionTokens)
}

func TestTrajectoryStore_SessionCountAfterClose(t *testing.T) {
	tmpDir := t.TempDir()

	store := monitoring.NewTrajectoryStore(monitoring.TrajectoryStoreConfig{
		Enabled:   true,
		BaseDir:   tmpDir,
		AgentName: "test-agent",
	})

	// Create multiple sessions
	store.RecordUserMessage("session-1", "Hello 1")
	store.RecordAgentResponse("session-1", monitoring.AgentResponseData{Message: "Hi 1"})
	store.RecordUserMessage("session-2", "Hello 2")
	store.RecordAgentResponse("session-2", monitoring.AgentResponseData{Message: "Hi 2"})
	store.RecordUserMessage("session-3", "Hello 3")
	store.RecordAgentResponse("session-3", monitoring.AgentResponseData{Message: "Hi 3"})

	assert.Equal(t, 3, store.GetSessionCount())

	// Close should clean up all sessions
	err := store.Close()
	require.NoError(t, err)

	// After close, session count should be 0 (maps cleared)
	assert.Equal(t, 0, store.GetSessionCount())
}
