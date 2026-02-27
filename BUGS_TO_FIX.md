# Context Gateway - Bug Tracking & Fix Plan

> Generated from security audit on 2026-02-23

## Priority Legend
- **P0**: Critical security/data isolation issues - fix immediately
- **P1**: High security risk - fix before any release
- **P2**: Performance/correctness issues - fix soon
- **P3**: Code quality/maintainability - fix when convenient

---

## P0: Critical Issues (Cross-Session Data Leakage)

### BUG-001: Cross-Session Auth Token Leakage in Preemptive Summarizer
- **Files**: `internal/preemptive/summarizer.go`, `internal/preemptive/manager.go`, `internal/gateway/handler.go`
- **Lines**: summarizer.go:21-24, summarizer.go:57, manager.go:64-66, handler.go:249-252
- **Problem**: Auth token and endpoint are stored globally on `Summarizer` struct, then reused across all background jobs. One user's captured Bearer token can be used for another user's summarization.
- **Impact**: Credential isolation failure - requests can execute with wrong user's auth
- **Fix**: 
  - Add `AuthToken`, `AuthEndpoint` fields to `Job` struct
  - Pass auth metadata into `Worker.Submit()`
  - Use per-job credentials in `callAPI()` instead of global `s.capturedAuth`
- **Status**: [x] FIXED - Added per-job auth fields to Job struct, pass auth from request headers via Weak Hashing + Fuzzy Matching
- **Files**: `internal/preemptive/session.go`, `internal/preemptive/manager.go`
- **Lines**: session.go:64-85, manager.go:126-165, session.go:254-289
- **Problem**: Session ID is only 16 hex chars from first user message hash. Fuzzy matching can remap compaction requests to different sessions with "similar" message counts.
- **Impact**: Summary/auth/cost state can bleed between unrelated sessions
- **Fix**:
  - Require explicit session ID from client header (e.g., `X-Session-ID`)
  - Fall back to full conversation hash (not truncated)
  - Disable fuzzy matching unless same trusted session identifier
- **Status**: [x] FIXED - Increased hash to 32 chars, added X-Session-ID header support, optional fuzzy matching disable

### BUG-003: Streaming Fallback Masks Upstream Errors as HTTP 200
- **Files**: `internal/gateway/handler.go`
- **Lines**: handler.go:649, handler.go:692, handler.go:717
- **Problem**: `flushBufferedResponse()` always writes `http.StatusOK` regardless of actual upstream status code.
- **Impact**: Clients see success for failed requests, breaking retry logic and monitoring
- **Fix**: 
  - Change `flushBufferedResponse(w, headers, preemptiveHeaders, chunks)` signature to include `statusCode int`
  - Pass `resp.StatusCode` through and write it instead of hardcoded 200
- **Status**: [x] FIXED - flushBufferedResponse now accepts statusCode parameter

---

## P1: High Security Risks

### BUG-004: Unauthenticated /expand and /costs Endpoints
- **Files**: `internal/gateway/gateway.go`, `internal/gateway/handler.go`
- **Lines**: gateway.go:293, handler.go:117, handler.go:1690
- **Problem**: Management endpoints exposed without authentication on all interfaces
- **Impact**: Anyone with network access can retrieve shadow context or view cost data
- **Fix**:
  - Add config option `management_interface: "127.0.0.1"` (default localhost-only)
  - Or require Bearer token for management endpoints
- **Status**: [ ] Not Started

### BUG-005: SSRF via localhost in Default Allowlist
- **Files**: `internal/gateway/gateway.go`, `internal/gateway/middleware.go`
- **Lines**: gateway.go:86-87, middleware.go:281-292
- **Problem**: `localhost` and `127.0.0.1` in default `allowedHosts` enables SSRF to local services
- **Impact**: Attacker can pivot to internal services via X-Target-URL
- **Fix**:
  - Remove localhost from default allowlist
  - Add `dev_mode: true` config flag to explicitly enable local targets
  - Validate URL scheme is https:// in production mode
- **Status**: [x] FIXED - Removed localhost, added GATEWAY_ALLOW_LOCAL env var for dev mode

### BUG-006: HTML Injection in Cost Dashboard
- **Files**: `internal/costcontrol/dashboard.go`
- **Lines**: dashboard.go:125+
- **Problem**: Model/session names rendered without escaping in HTML template
- **Impact**: XSS if attacker controls model name in request
- **Fix**: Use `html/template` package with auto-escaping instead of `fmt.Sprintf`
- **Status**: [ ] Not Started

### BUG-007: Unsigned Binary Updates
- **Files**: `cmd/updater.go`
- **Lines**: updater.go:174-200, updater.go:215
- **Problem**: Downloaded release binary is installed without signature/checksum verification
- **Impact**: Supply chain attack - malicious binary could be installed
- **Fix**:
  - Download and verify `.sha256` checksum file from release
  - Consider GPG signature verification for releases
- **Status**: [ ] Not Started

### BUG-008: Hardcoded OpenClaw Token
- **Files**: `cmd/agent_utils.go`
- **Lines**: agent_utils.go:552
- **Problem**: Fixed token `"localdev"` used for OpenClaw gateway
- **Impact**: Predictable credential (low risk as local-only)
- **Fix**: Generate random token per session using `crypto/rand`
- **Status**: [x] FIXED - Now generates random 16-byte hex token per session

---

## P2: Performance Issues

### BUG-009: Full Stream Buffering for Expand Detection
- **Files**: `internal/gateway/handler.go`
- **Lines**: handler.go:610-630
- **Problem**: Entire streaming response buffered in memory before checking for expand_context calls
- **Impact**: High memory usage, latency spikes on large responses
- **Fix**:
  - Implement ring buffer with bounded size
  - Flush early portions when no phantom tool markers detected in initial chunks
- **Status**: [ ] Not Started

### BUG-010: Unbounded io.ReadAll on Upstream Responses
- **Files**: `internal/gateway/handler.go`, `internal/gateway/phantom_loop.go`
- **Lines**: handler.go:442, phantom_loop.go:102
- **Problem**: No size limit on upstream response body reads
- **Impact**: Memory exhaustion on large/malicious responses
- **Fix**: Wrap with `io.LimitReader(resp.Body, maxResponseSize)` - suggest 50MB default
- **Status**: [x] FIXED - Added MaxResponseSize (50MB) limit to all io.ReadAll calls

### BUG-011: O(n) Budget Check Under Lock
- **Files**: `internal/costcontrol/tracker.go`
- **Lines**: tracker.go:42+
- **Problem**: `CheckBudget()` scans all sessions under mutex on every request
- **Impact**: Throughput degradation as session count grows
- **Fix**: Maintain atomic global accumulator updated in `RecordUsage()`
- **Status**: [x] FIXED - Added atomic globalCostNano counter, O(1) CheckBudget()

### BUG-012: Duplicate Request Body Parsing
- **Files**: `internal/gateway/router.go`
- **Lines**: router.go:79-95
- **Problem**: Tool extraction happens at route time and again in pipe processing
- **Impact**: Redundant JSON parsing overhead
- **Fix**: Cache extracted artifacts in `PipelineContext`, reuse in pipes
- **Status**: [ ] Not Started

---

## P3: Code Quality Issues

### BUG-013: URL Path Append Logic Corrupts Query Strings
- **Files**: `internal/gateway/handler.go`
- **Lines**: handler.go:980-984
- **Problem**: String concatenation for URL path can break query parameters
- **Impact**: Subtle request failures with certain URL patterns
- **Fix**: Use `url.Parse()` and proper path joining with query preservation
- **Status**: [ ] Not Started

### BUG-014: Global "default" Cost Session Fallback
- **Files**: `internal/gateway/handler.go`
- **Lines**: handler.go:232-235
- **Problem**: Requests without user messages share single "default" budget bucket
- **Impact**: Unrelated requests can block each other on shared budget
- **Fix**: Use request ID or client IP as fallback session identifier
- **Status**: [ ] Not Started

### BUG-015: Dropped Context in Compression Pipeline
- **Files**: `internal/pipes/tool_output/tool_output.go`
- **Lines**: tool_output.go:589
- **Problem**: `context.Background()` used for external LLM call instead of request context
- **Impact**: Work continues after client disconnect, wasting resources
- **Fix**: Thread request context through pipeline, use for `external.CallLLM()`
- **Status**: [ ] Not Started

### BUG-016: Duplicate Phantom Tool Response Filtering
- **Files**: `internal/gateway/expand_context_handler.go`, `internal/gateway/search_tool_handler.go`
- **Lines**: expand_context_handler.go:137+, search_tool_handler.go:521+
- **Problem**: Nearly identical response walking/filtering code in two handlers
- **Impact**: Maintenance burden, bug risk
- **Fix**: Extract shared `filterToolCalls(response, toolName, provider)` utility
- **Status**: [ ] Not Started

### BUG-017: Duplicate Session ID Hashing
- **Files**: `internal/preemptive/utils.go`, `internal/preemptive/session.go`
- **Lines**: utils.go:23-27, session.go:64-85
- **Problem**: Session ID computation duplicated in two places
- **Impact**: Inconsistency risk
- **Fix**: Single `SessionIDProvider` interface used everywhere
- **Status**: [ ] Not Started

### BUG-018: Missing Shutdown Hooks for Background Goroutines
- **Files**: Multiple stores/managers
- **Lines**: tool_session.go:50, tracker.go:25, session.go:53, manager.go:55
- **Problem**: Background cleanup goroutines not stopped on shutdown
- **Impact**: Goroutine leaks, unclean shutdown
- **Fix**: Add `Stop()` methods and call from `Gateway.Shutdown()`
- **Status**: [ ] Not Started

---

## Testing Gaps

### TEST-001: Cross-Session Auth Leakage Test
- **Missing**: Concurrent test with two different auth tokens verifying no cross-use

### TEST-002: Streaming Status Code Preservation Test
- **Missing**: Unit tests for 429/500 upstream through streaming paths

### TEST-003: Management Endpoint Access Control Test
- **Missing**: HTTP tests verifying /expand and /costs require auth or localhost

### TEST-004: Dashboard XSS Test
- **Missing**: Test malicious model/session names are escaped

### TEST-005: Response Size Guardrail Test
- **Missing**: Oversized upstream response triggers controlled failure

### TEST-006: Updater Verification Test
- **Missing**: Test that tampered downloads are rejected

---

## Fix Order Plan

### Phase 1: Critical Security (P0)
1. BUG-001 - Auth token isolation
2. BUG-002 - Session ID collision
3. BUG-003 - Streaming status codes

### Phase 2: High Security (P1)
4. BUG-005 - SSRF localhost removal
5. BUG-004 - Management endpoint auth
6. BUG-006 - Dashboard XSS
7. BUG-007 - Signed updates

### Phase 3: Performance (P2)
8. BUG-010 - Response size limits
9. BUG-011 - Budget check optimization
10. BUG-009 - Streaming buffer optimization

### Phase 4: Code Quality (P3)
11. BUG-013 through BUG-018

---

## Notes
- All bugs verified present in current codebase
- `.env` file is NOT committed (verified safe)
- Public repo has no sensitive data exposure
