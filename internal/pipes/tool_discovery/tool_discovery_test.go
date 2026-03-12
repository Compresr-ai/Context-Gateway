package tooldiscovery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/adapters"
)

// makeTool is a helper for building ExtractedContent with ToolName as ID.
func makeTool(name string) adapters.ExtractedContent {
	return adapters.ExtractedContent{
		ID:       name,
		ToolName: name,
	}
}

// makeTools builds a slice of tools from names.
func makeTools(names ...string) []adapters.ExtractedContent {
	tools := make([]adapters.ExtractedContent, len(names))
	for i, n := range names {
		tools[i] = makeTool(n)
	}
	return tools
}

// keptToolNames returns names of tools with Keep=true from CompressedResult slice.
func keptToolNames(results []adapters.CompressedResult) []string {
	names := make([]string, 0)
	for _, r := range results {
		if r.Keep {
			names = append(names, r.ID) // ID == ToolName in tests
		}
	}
	return names
}

// newTestPipe creates a Pipe with minimal config for unit testing.
func newTestPipe(maxTools int, alwaysKeep []string) *Pipe {
	ak := make(map[string]bool, len(alwaysKeep))
	for _, n := range alwaysKeep {
		ak[n] = true
	}
	return &Pipe{
		enabled:    true,
		strategy:   "relevance",
		minTools:   1,
		maxTools:   maxTools,
		targetRatio: 1.0, // ratio=1 means cap is driven by maxTools
		alwaysKeep: ak,
	}
}

// TestAlwaysKeepSurvivedCap verifies always_keep tools are kept even when the
// slot budget is already exhausted by recently_used tools.
func TestAlwaysKeepSurvivedCap(t *testing.T) {
	// 10 recently-used tools + 3 always-keep tools, max = 5
	recentNames := []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9", "r10"}
	alwaysNames := []string{"ak1", "ak2", "ak3"}

	all := append(makeTools(recentNames...), makeTools(alwaysNames...)...)
	recentMap := make(map[string]bool)
	for _, n := range recentNames {
		recentMap[n] = true
	}

	p := newTestPipe(5, alwaysNames)

	out := p.scoreAndFilterTools(&filterInput{
		tools:         all,
		query:         "",
		recentTools:   recentMap,
		expandedTools: map[string]bool{},
	})

	// All 3 always-keep tools must be kept.
	for _, name := range alwaysNames {
		assert.Contains(t, out.keptNames, name, "always_keep tool %q must be kept", name)
	}
}

// TestAlwaysKeepNotDeferred verifies always_keep tools never appear in deferred list.
func TestAlwaysKeepNotDeferred(t *testing.T) {
	tools := makeTools("ak1", "other1", "other2", "other3", "other4", "other5")
	p := newTestPipe(3, []string{"ak1"})

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	assert.NotContains(t, out.deferredNames, "ak1", "always_keep tool must not appear in deferred list")
	assert.Contains(t, out.keptNames, "ak1")
}

// TestExpandedToolsAlwaysKept verifies expanded (search-found) tools are always kept.
func TestExpandedToolsAlwaysKept(t *testing.T) {
	tools := makeTools("expanded1", "r1", "r2", "r3", "r4", "r5")
	recentMap := map[string]bool{"r1": true, "r2": true, "r3": true, "r4": true, "r5": true}
	expandedMap := map[string]bool{"expanded1": true}

	p := newTestPipe(3, nil)

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   recentMap,
		expandedTools: expandedMap,
	})

	assert.Contains(t, out.keptNames, "expanded1")
	assert.NotContains(t, out.deferredNames, "expanded1")
}

// TestCandidatesFilledByScore verifies that non-protected tools fill remaining
// slots in score order.
func TestCandidatesFilledByScore(t *testing.T) {
	// Tools: "exact_match" should score higher due to query keyword match.
	tools := makeTools("exact_match", "irrelevant1", "irrelevant2", "irrelevant3")
	p := newTestPipe(2, nil)

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "exact_match",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	require.Equal(t, 2, out.keptCount)
	// exact_match has the highest score and should be kept.
	assert.Contains(t, out.keptNames, "exact_match")
}

// TestAlwaysKeepMoreThanMax verifies that when there are more always_keep tools
// than max_tools, all of them are still kept (cap is exceeded gracefully).
func TestAlwaysKeepMoreThanMax(t *testing.T) {
	alwaysNames := []string{"ak1", "ak2", "ak3", "ak4", "ak5"}
	tools := makeTools(alwaysNames...)

	p := newTestPipe(2, alwaysNames) // max=2, but 5 always-keep

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	// All always-keep tools must be present.
	for _, name := range alwaysNames {
		assert.Contains(t, out.keptNames, name)
	}
	assert.Empty(t, out.deferredNames)
}

// TestNoAlwaysKeep verifies normal scoring/cap behaviour without always_keep.
func TestNoAlwaysKeep(t *testing.T) {
	tools := makeTools("t1", "t2", "t3", "t4", "t5")
	p := newTestPipe(3, nil)

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	assert.Equal(t, 3, out.keptCount)
	assert.Equal(t, 2, len(out.deferredNames))
}

// TestResultsIDsComplete verifies that every input tool appears exactly once
// in the output results (either kept or deferred).
func TestResultsIDsComplete(t *testing.T) {
	allNames := []string{"ak1", "r1", "r2", "r3", "other1", "other2"}
	tools := makeTools(allNames...)
	p := newTestPipe(3, []string{"ak1"})

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{"r1": true, "r2": true},
		expandedTools: map[string]bool{},
	})

	// All IDs must appear in results exactly once.
	seen := make(map[string]int)
	for _, r := range out.results {
		seen[r.ID]++
	}
	for _, name := range allNames {
		assert.Equal(t, 1, seen[name], "tool %q should appear exactly once in results", name)
	}

	// keptNames + deferredNames must cover all tools.
	assert.Equal(t, len(allNames), len(out.keptNames)+len(out.deferredNames))
}

// TestEmptyInput verifies no panic and empty output when there are no tools.
func TestEmptyInput(t *testing.T) {
	p := newTestPipe(5, []string{"ak1"})

	out := p.scoreAndFilterTools(&filterInput{
		tools:         []adapters.ExtractedContent{},
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	assert.Equal(t, 0, out.keptCount)
	assert.Empty(t, out.keptNames)
	assert.Empty(t, out.deferredNames)
	assert.Empty(t, out.results)
}

// TestToolInBothAlwaysKeepAndExpanded verifies no duplication when a tool appears
// in both always_keep and expandedTools — it must appear exactly once in results.
func TestToolInBothAlwaysKeepAndExpanded(t *testing.T) {
	tools := makeTools("dual", "other1", "other2")
	p := newTestPipe(5, []string{"dual"})

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{"dual": true},
	})

	seen := 0
	for _, r := range out.results {
		if r.ID == "dual" {
			seen++
			assert.True(t, r.Keep)
		}
	}
	assert.Equal(t, 1, seen, "tool in both always_keep and expanded must appear exactly once")
}

// TestAlwaysKeepExceedsCapWithCandidates verifies that when protected tools fill the
// entire cap, all candidates are deferred (not silently dropped or panicked).
func TestAlwaysKeepExceedsCapWithCandidates(t *testing.T) {
	// 4 always_keep, max=2, plus 3 regular candidates.
	alwaysNames := []string{"ak1", "ak2", "ak3", "ak4"}
	tools := append(makeTools(alwaysNames...), makeTools("c1", "c2", "c3")...)
	p := newTestPipe(2, alwaysNames)

	out := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         "",
		recentTools:   map[string]bool{},
		expandedTools: map[string]bool{},
	})

	// All always_keep kept despite exceeding cap.
	for _, name := range alwaysNames {
		assert.Contains(t, out.keptNames, name)
	}
	// All candidates deferred.
	assert.Contains(t, out.deferredNames, "c1")
	assert.Contains(t, out.deferredNames, "c2")
	assert.Contains(t, out.deferredNames, "c3")
	// Total coverage.
	assert.Equal(t, 7, len(out.keptNames)+len(out.deferredNames))
}
