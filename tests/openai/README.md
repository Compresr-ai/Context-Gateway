# OpenAI Integration Tests

This folder contains integration tests for OpenAI API compatibility through the Context Gateway.

## Test Structure

```
tests/openai/
└── integration/
    ├── e2e_test.go           # End-to-end tests (12 tests)
    ├── expand_behavior_test.go # expand_context feature tests (9 tests)
    └── hard_integration_test.go # Edge cases and complex scenarios (11 tests)
```

## Test Files

### e2e_test.go (12 tests)

Basic end-to-end tests validating gateway functionality with OpenAI.

| Test Name | Description |
|-----------|-------------|
| `TestE2E_SimpleChat` | Basic chat request without tools |
| `TestE2E_UsageExtraction` | Validates usage metrics are extracted correctly |
| `TestE2E_SmallToolResult` | Small tool output passes through unchanged |
| `TestE2E_LargeToolResultCompression` | Large tool output gets compressed |
| `TestE2E_MultipleToolResults` | Multiple tool results in single request |
| `TestE2E_DirectoryListing` | Large directory listing compression |
| `TestE2E_BashCommandOutput` | Shell command output handling |
| `TestE2E_ErrorToolResult` | Tool error message handling |
| `TestE2E_GitDiffOutput` | Git diff output compression |
| `TestE2E_JSONToolOutput` | JSON-formatted tool result handling |
| `TestE2E_HealthCheck` | Gateway health endpoint verification |
| `TestE2E_WithSystemPrompt` | System prompt preservation |

### expand_behavior_test.go (9 tests)

Tests for the `expand_context` feature - the phantom tool that allows LLMs to request full content from compressed summaries.

| Test Name | Description |
|-----------|-------------|
| `TestExpandBehavior_WithExpand_MinimalCompression` | Validates compression with expand_context enabled |
| `TestExpandBehavior_WithExpand_QuestionRequiresDetail` | Tests expand trigger with detailed questions |
| `TestExpandBehavior_SmallContent_NoCompression` | Small content bypasses compression |
| `TestExpandBehavior_NoExpand_CompressedOnly` | Compressed output without expand option |
| `TestExpandBehavior_NoExpand_LargeOutput` | Large output without expand availability |
| `TestExpandBehavior_Compare_DetailedQuestionWithVsWithout` | Side-by-side comparison with/without expand |
| `TestExpandBehavior_MultiTool_SomeNeedExpand` | Mixed small/large tool outputs |
| `TestExpandBehavior_WithExpand_ErrorToolResult` | Error messages with expand enabled |
| `TestExpandBehavior_StressTest_ManyToolResults` | Performance with 10 concurrent tool results |

### hard_integration_test.go (11 tests)

Edge cases, complex scenarios, and real-world output formats.

| Test Name | Description |
|-----------|-------------|
| `TestHardIntegration_ThreeToolsOneLarge_OneExpandNeeded` | Mixed tool output sizes |
| `TestHardIntegration_ThreeToolsAllLarge` | All large tool outputs |
| `TestHardIntegration_LargeErrorMessage` | Large stack trace error handling |
| `TestHardIntegration_RealWorld_GitLog` | Real git log output |
| `TestHardIntegration_RealWorld_NPMInstall` | npm install output |
| `TestHardIntegration_RealWorld_DockerBuild` | Docker build output |
| `TestHardIntegration_EmptyToolResult` | Empty tool result handling |
| `TestHardIntegration_SpecialCharactersInOutput` | Unicode, emoji, escape characters |

## Running Tests

```bash
# Run all OpenAI tests
go test ./tests/openai/integration/... -v

# Run specific test file
go test ./tests/openai/integration/... -v -run TestE2E
go test ./tests/openai/integration/... -v -run TestExpandBehavior
go test ./tests/openai/integration/... -v -run TestHardIntegration

# Run single test
go test ./tests/openai/integration/... -v -run TestE2E_SimpleChat
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OPENAI_API_KEY` | OpenAI API key (required) |
| `COMPRESR_API_URL` | Compresr compression API URL |
| `COMPRESR_API_KEY` | Compresr API key |

## Configuration

Tests use `gpt-5-nano` model for cost-effective testing. Configuration patterns:

- **Passthrough**: No compression, direct API proxy
- **Compression**: Tool output compression enabled (MinBytes: 500)
- **Expand Enabled**: `EnableExpandContext: true` - adds phantom tool
- **Expand Disabled**: `EnableExpandContext: false` - compression only

## Notes

- Tests will skip if `OPENAI_API_KEY` is not set
- Uses retry logic for rate limiting (3 retries with exponential backoff)
- Timeouts vary from 30s (simple) to 120s (stress tests)
