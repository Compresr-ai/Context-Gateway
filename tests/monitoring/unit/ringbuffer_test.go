package unit

import (
	"fmt"
	"sync"
	"testing"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRingBuffer_OverflowWraparound(t *testing.T) {
	rb := monitoring.NewRingBuffer[int](5)

	// Insert 10 items into a buffer of size 5
	for i := 0; i < 10; i++ {
		rb.Record(i)
	}

	assert.Equal(t, 5, rb.Count())

	// Recent should return newest items first
	recent := rb.Recent(5)
	require.Len(t, recent, 5)
	assert.Equal(t, 9, recent[0]) // newest
	assert.Equal(t, 5, recent[4]) // oldest remaining

	// All should return in order oldest to newest
	all := rb.All()
	require.Len(t, all, 5)
	assert.Equal(t, 5, all[0])
	assert.Equal(t, 9, all[4])
}

func TestRingBuffer_ConcurrentRecordAndRecent(t *testing.T) {
	rb := monitoring.NewRingBuffer[int](100)

	var wg sync.WaitGroup

	// 50 writers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			rb.Record(val)
		}(i)
	}

	// 50 readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rb.Recent(10)
			rb.All()
			rb.Count()
		}()
	}

	wg.Wait()
	assert.Equal(t, 50, rb.Count())
}

func TestRingBuffer_RecentMoreThanCount(t *testing.T) {
	rb := monitoring.NewRingBuffer[string](10)
	rb.Record("a")
	rb.Record("b")
	rb.Record("c")

	// Request more than available
	recent := rb.Recent(100)
	assert.Len(t, recent, 3)
	assert.Equal(t, "c", recent[0])
	assert.Equal(t, "a", recent[2])
}

func TestRingBuffer_RecentZeroOrNegative(t *testing.T) {
	rb := monitoring.NewRingBuffer[int](10)
	rb.Record(1)

	assert.Nil(t, rb.Recent(0))
	assert.Nil(t, rb.Recent(-1))
}

func TestRingBuffer_EmptyBuffer(t *testing.T) {
	rb := monitoring.NewRingBuffer[int](10)

	assert.Equal(t, 0, rb.Count())
	assert.Nil(t, rb.Recent(5))
	assert.Nil(t, rb.All())
}

func TestRingBuffer_ResetDuringConcurrentAccess(t *testing.T) {
	rb := monitoring.NewRingBuffer[string](50)

	// Pre-populate
	for i := 0; i < 50; i++ {
		rb.Record(fmt.Sprintf("item_%d", i))
	}

	var wg sync.WaitGroup

	// Readers
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rb.Recent(10)
			rb.Count()
		}()
	}

	// Reset
	wg.Add(1)
	go func() {
		defer wg.Done()
		rb.Reset()
	}()

	// Writers
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rb.Record(fmt.Sprintf("new_%d", i))
		}(i)
	}

	wg.Wait()
	// No panics = success
}

func TestRingBuffer_SingleCapacity(t *testing.T) {
	rb := monitoring.NewRingBuffer[int](1)

	rb.Record(1)
	assert.Equal(t, 1, rb.Count())

	rb.Record(2)
	assert.Equal(t, 1, rb.Count())

	recent := rb.Recent(1)
	require.Len(t, recent, 1)
	assert.Equal(t, 2, recent[0])
}
