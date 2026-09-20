package tool

import (
	"context"
	"fmt"
	"sync"
)

// Parallel runs fn for every item with at most n running at once (n < 1
// means 1). Waiting for a slot honours ctx, so no new item starts after ctx
// is done or after the first error, which also cancels the ctx passed to the
// running calls. A panic in fn becomes an error wrapping [ErrInternal]
// instead of killing the process. Parallel waits for every started call and
// returns the first error (by time), or ctx.Err() when ctx ended before every
// item started.
func Parallel[T any](ctx context.Context, n int, items []T, fn func(ctx context.Context, i int, item T) error) error {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	fail := func(err error) {
		once.Do(func() {
			firstErr = err
			cancel()
		})
	}
	sem := make(chan struct{}, n)
	started := 0
loop:
	for i, item := range items {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}
		// A slot and cancellation can be ready together; never start late work.
		if ctx.Err() != nil {
			<-sem
			break
		}
		started++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					fail(fmt.Errorf("%w: panic in parallel item %d: %v", ErrInternal, i, p))
				}
			}()
			if err := fn(ctx, i, item); err != nil {
				fail(err)
			}
		}()
	}
	wg.Wait()
	if started < len(items) {
		once.Do(func() { firstErr = ctx.Err() })
	}
	return firstErr
}
