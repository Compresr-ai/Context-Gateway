package performance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/store"
)

// =============================================================================
// MEMORY FOOTPRINT TESTS
// =============================================================================

func TestMemoryFootprint(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory footprint test in short mode")
	}

	// Force garbage collection before test
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	baselineAlloc := m1.Alloc

	// Create store with dual TTL
	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	// Simulate 100 requests with compression
	for i := 0; i < 100; i++ {
		sessionID := fmt.Sprintf("session-%d", i%10) // 10 unique sessions

		// Create session
		sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

		// Store original data
		originalData := string(bytes.Repeat([]byte("test message data "), 1000)) // ~18KB
		st.Set(sessionID, originalData)

		// Store compressed data
		compressedData := string(bytes.Repeat([]byte("compressed"), 100)) // ~1KB
		st.SetCompressed(sessionID, compressedData)

		// Update session with token counts
		sm.Update(sessionID, func(s *preemptive.Session) {
			s.LastKnownTokens = 1000
			s.UsagePercent = 0.75
		})
	}

	// Force GC and measure
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	memUsed := (m2.Alloc - baselineAlloc) / (1024 * 1024) // MB

	stats := sm.Stats()
	t.Logf("📊 Memory footprint after 100 requests:")
	t.Logf("   Base memory: %d MB", baselineAlloc/(1024*1024))
	t.Logf("   Final memory: %d MB", m2.Alloc/(1024*1024))
	t.Logf("   Memory used: %d MB", memUsed)
	t.Logf("   Total sessions: %v", stats["total_sessions"])
	t.Logf("   Active sessions: %v", stats["active_sessions"])

	// Assert memory stays under 50MB per 100 requests
	assert.LessOrEqual(t, memUsed, uint64(50), "Memory footprint should be ≤50MB per 100 requests")

	// Verify session limit is working
	totalSessions, _ := stats["total_sessions"].(int)
	assert.LessOrEqual(t, totalSessions, 500, "Sessions should respect 500 limit")
}

// =============================================================================
// CPU USAGE TESTS
// =============================================================================

func TestCPUUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping CPU usage test in short mode")
	}

	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	// Measure CPU time before
	var r1 runtime.MemStats
	runtime.ReadMemStats(&r1)

	start := time.Now()

	// Simulate rapid requests
	for i := 0; i < 1000; i++ {
		sessionID := fmt.Sprintf("session-%d", i%20)
		sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

		originalData := string(bytes.Repeat([]byte("data"), 1000))
		st.Set(sessionID, originalData)

		compressedData := string(bytes.Repeat([]byte("comp"), 200))
		st.SetCompressed(sessionID, compressedData)

		sm.Update(sessionID, func(s *preemptive.Session) {
			s.LastKnownTokens = 2000
			s.UsagePercent = 0.50
		})
	}

	elapsed := time.Since(start)
	throughput := float64(1000) / elapsed.Seconds()

	var r2 runtime.MemStats
	runtime.ReadMemStats(&r2)

	t.Logf("⚡ CPU Performance:")
	t.Logf("   Time: %v", elapsed)
	t.Logf("   Throughput: %.2f req/sec", throughput)
	t.Logf("   GC runs: %d", r2.NumGC-r1.NumGC)
	t.Logf("   GC pause total: %v", time.Duration(r2.PauseTotalNs-r1.PauseTotalNs))

	// Should process at least 100 requests per second
	assert.GreaterOrEqual(t, throughput, 100.0, "Should handle ≥100 req/sec")

	// GC pauses should be reasonable
	gcPauseMs := float64(r2.PauseTotalNs-r1.PauseTotalNs) / 1e6
	assert.LessOrEqual(t, gcPauseMs, 100.0, "Total GC pause should be ≤100ms")
}

// =============================================================================
// STRESS TESTS
// =============================================================================

func TestStressLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate OpenAI API response
		time.Sleep(10 * time.Millisecond) // Simulate processing

		response := map[string]interface{}{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": "Test response",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Stress test: 50 concurrent goroutines, 20 requests each = 1000 total
	concurrency := 50
	requestsPerWorker := 20
	totalRequests := concurrency * requestsPerWorker

	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := 0
	successCount := 0

	start := time.Now()

	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < requestsPerWorker; i++ {
				sessionID := fmt.Sprintf("stress-session-%d", workerID)

				// Create or get session
				sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

				// Make HTTP request
				reqBody := map[string]interface{}{
					"model": "gpt-4",
					"messages": []map[string]string{
						{"role": "user", "content": "test"},
					},
				}
				body, _ := json.Marshal(reqBody)

				resp, err := http.Post(server.URL, "application/json", bytes.NewReader(body))
				if err != nil {
					mu.Lock()
					errors++
					mu.Unlock()
					continue
				}

				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				// Store in cache
				st.Set(sessionID, string(body))
				st.SetCompressed(sessionID, string(respBody))

				// Update session
				sm.Update(sessionID, func(s *preemptive.Session) {
					s.LastKnownTokens = 2000
					s.UsagePercent = 0.50
				})

				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(c)
	}

	wg.Wait()
	elapsed := time.Since(start)
	throughput := float64(totalRequests) / elapsed.Seconds()

	t.Logf("🔥 Stress Test Results:")
	t.Logf("   Total requests: %d", totalRequests)
	t.Logf("   Concurrency: %d workers", concurrency)
	t.Logf("   Time: %v", elapsed)
	t.Logf("   Throughput: %.2f req/sec", throughput)
	t.Logf("   Success: %d", successCount)
	t.Logf("   Errors: %d", errors)
	t.Logf("   Error rate: %.2f%%", float64(errors)/float64(totalRequests)*100)

	// Assertions
	assert.GreaterOrEqual(t, throughput, 100.0, "Should maintain ≥100 req/sec under load")
	errorRate := float64(errors) / float64(totalRequests)
	assert.LessOrEqual(t, errorRate, 0.01, "Error rate should be <1%")

	// Check memory didn't explode
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memMB := m.Alloc / (1024 * 1024)
	t.Logf("   Final memory: %d MB", memMB)
	assert.LessOrEqual(t, memMB, uint64(200), "Memory should stay under 200MB during stress")
}

// =============================================================================
// MEMORY LEAK TESTS
// =============================================================================

func TestMemoryLeaks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	// Run multiple cycles
	cycles := 5
	memSnapshots := make([]uint64, 0, cycles)

	for cycle := 0; cycle < cycles; cycle++ {
		// Process requests
		for i := 0; i < 200; i++ {
			sessionID := fmt.Sprintf("leak-test-%d", i)
			sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

			data := string(bytes.Repeat([]byte("leak test data "), 500))
			st.Set(sessionID, data)
			st.SetCompressed(sessionID, data[:100])

			sm.Update(sessionID, func(s *preemptive.Session) {
				s.LastKnownTokens = 1000
				s.UsagePercent = 0.50
			})
		}

		// Force cleanup and GC
		runtime.GC()
		time.Sleep(2 * time.Second) // Allow cleanup to run

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		memSnapshots = append(memSnapshots, m.Alloc)

		t.Logf("Cycle %d: Memory = %d MB", cycle+1, m.Alloc/(1024*1024))
	}

	// Check that memory doesn't continuously grow
	// Allow for some variance, but later cycles shouldn't be significantly higher
	firstCycleAvg := (memSnapshots[0] + memSnapshots[1]) / 2
	lastCycleAvg := (memSnapshots[3] + memSnapshots[4]) / 2

	growthRatio := float64(lastCycleAvg) / float64(firstCycleAvg)
	t.Logf("Memory growth ratio: %.2fx", growthRatio)

	// Memory should stabilize - no more than 1.5x growth from first to last cycles
	assert.LessOrEqual(t, growthRatio, 1.5, "Memory leak detected - memory should stabilize")
}

// =============================================================================
// GOROUTINE LEAK TESTS
// =============================================================================

func TestGoroutineLeaks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping goroutine leak test in short mode")
	}

	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	baselineGoroutines := runtime.NumGoroutine()
	t.Logf("Baseline goroutines: %d", baselineGoroutines)

	// Simulate concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("goroutine-test-%d", id)
			sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

			data := "test data"
			st.Set(sessionID, data)
			st.SetCompressed(sessionID, data)

			sm.Update(sessionID, func(s *preemptive.Session) {
				s.LastKnownTokens = 100
				s.UsagePercent = 0.50
			})
		}(i)
	}
	wg.Wait()

	// Allow cleanup goroutines to finish
	time.Sleep(3 * time.Second)
	runtime.GC()

	finalGoroutines := runtime.NumGoroutine()
	t.Logf("Final goroutines: %d", finalGoroutines)

	goroutineIncrease := finalGoroutines - baselineGoroutines
	t.Logf("Goroutine increase: %d", goroutineIncrease)

	// Should not leak more than 5 goroutines (allowing for cleanup workers)
	assert.LessOrEqual(t, goroutineIncrease, 5, "Possible goroutine leak detected")
}

// =============================================================================
// CONCURRENT SESSION TESTS
// =============================================================================

func TestConcurrentSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent session test in short mode")
	}

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	// Test concurrent access to same session
	sessionID := "concurrent-test"
	sm.GetOrCreateSession(sessionID, "gpt-4", 128000)

	var wg sync.WaitGroup
	concurrency := 100
	operationsPerWorker := 50

	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < operationsPerWorker; i++ {
				// Randomly perform different operations
				switch i % 4 {
				case 0:
					sm.Update(sessionID, func(s *preemptive.Session) {
						s.LastKnownTokens = 2000
						s.UsagePercent = 0.50
					})
				case 1:
					sm.SetSummaryReady(sessionID, "Summary", 500, 10, 20)
				case 2:
					sm.Get(sessionID)
				case 3:
					sm.IsSummaryValidForMessages(sessionID, 25)
				}
			}
		}()
	}

	wg.Wait()

	// Verify session is still consistent
	session := sm.Get(sessionID)
	require.NotNil(t, session, "Session should still exist")

	t.Logf("✅ Concurrent operations completed: %d workers × %d ops", concurrency, operationsPerWorker)
	t.Logf("   Session state: %s", session.State)

	// No explicit errors tracked - concurrent operations should complete without issues
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkSingleRequest(b *testing.B) {
	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	data := string(bytes.Repeat([]byte("benchmark data "), 1000))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sessionID := fmt.Sprintf("bench-%d", i%10)
		sm.GetOrCreateSession(sessionID, "gpt-4", 128000)
		st.Set(sessionID, data)
		st.SetCompressed(sessionID, data[:100])
		sm.Update(sessionID, func(s *preemptive.Session) {
			s.LastKnownTokens = 2000
			s.UsagePercent = 0.50
		})
	}
}

func BenchmarkConcurrentRequests(b *testing.B) {
	st := store.NewMemoryStoreWithDualTTL(5*time.Hour, 24*time.Hour)
	defer st.Close()

	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)
	defer sm.Close()

	data := string(bytes.Repeat([]byte("benchmark data "), 1000))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sessionID := fmt.Sprintf("bench-%d", i%10)
			sm.GetOrCreateSession(sessionID, "gpt-4", 128000)
			st.Set(sessionID, data)
			st.SetCompressed(sessionID, data[:100])
			sm.Update(sessionID, func(s *preemptive.Session) {
				s.LastKnownTokens = 2000
				s.UsagePercent = 0.50
			})
			i++
		}
	})
}
