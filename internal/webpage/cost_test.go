package webpage

import (
	"runtime"
	"time"
)

// Allocation bounds for the label cost tests, about 8× what the reader
// allocates.
const (
	// TestParseLabelCostBounded: each page is ~0.45 MB with one 200 000-rune
	// label shared by 2 000 controls. Computed once per node, a parse
	// allocates ~8-10 MB; computed once per control, ~3.97 GB.
	labelCostAllocBound = 80 << 20
	// TestParseLabelledByManyRefs: the page is ~0.2 MB, one control naming a
	// 300-rune node 100 000 times. Stopping at the label limit, a parse
	// allocates ~3-3.5 MB; joining every reference, ~400 MB (and still
	// ~80 MB with the node's text computed once: 100 000 × 401 bytes, built
	// up by doubling).
	labelledByAllocBound = 32 << 20
)

// allocated is the bytes run allocates. TotalAlloc counts the whole
// process, so the tests that use it must not run in parallel.
func allocated(run func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// growthOf is how many times longer large takes than small, each the best
// of three runs.
func growthOf(small, large func()) float64 {
	s, l := bestOf3(small), bestOf3(large)
	if s < time.Microsecond {
		s = time.Microsecond
	}
	return float64(l) / float64(s)
}

func bestOf3(run func()) time.Duration {
	best := time.Duration(-1)
	for range 3 {
		start := time.Now()
		run()
		if d := time.Since(start); best < 0 || d < best {
			best = d
		}
	}
	return best
}
