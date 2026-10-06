package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// PartConcurrency is the number of upload parts sent in parallel.
	PartConcurrency = 4
	// PartRetries is the number of retries per part after the first attempt.
	// Replaying a part is safe: each part is an idempotent PUT of fixed bytes.
	PartRetries = 3
	// MaxPartBytes bounds one upload operation; a part is buffered in memory
	// so the request carries a Content-Length.
	MaxPartBytes = 256 << 20

	partBackoff      = 500 * time.Millisecond
	maxPartBackoff   = 30 * time.Second
	partResponseCap  = 1 << 20
	maxPartErrorBody = 200
)

// PutParts sends every upload operation's byte range of file (size bytes)
// through fetch, which never adds Authorization. Up to [PartConcurrency]
// parts run in parallel, each retried up to [PartRetries] times with
// exponential backoff on clock. progress, if non-nil, receives cumulative
// bytes sent and is called serially.
func PutParts(ctx context.Context, fetch tool.Fetcher, clock tool.Clock, file io.ReaderAt, size int64, ops []Operation, progress func(done, total int64)) error {
	return putParts(ctx, partsJob{fetch: fetch, clock: clock, file: file, size: size, ops: ops, progress: progress})
}

// partsJob is one PutParts run; skip and onDone support resumable uploads.
type partsJob struct {
	fetch    tool.Fetcher
	clock    tool.Clock
	file     io.ReaderAt
	size     int64
	ops      []Operation
	progress func(done, total int64)
	// skip marks operation indices already uploaded.
	skip map[int]bool
	// onDone is called serially after operation i succeeded.
	onDone func(i int) error
}

func putParts(ctx context.Context, j partsJob) error {
	if err := ValidateOperations(j.ops, j.size); err != nil {
		return err
	}
	for i, op := range j.ops {
		if op.Length > MaxPartBytes {
			return fmt.Errorf("upload operation %d: part of %d bytes exceeds %d", i, op.Length, MaxPartBytes)
		}
	}
	if j.clock == nil {
		j.clock = tool.SystemClock{}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		done     int64
		firstErr error
	)
	pending := make(chan int)
	for i, op := range j.ops {
		if j.skip[i] {
			done += op.Length
		}
	}
	if j.progress != nil {
		j.progress(done, j.size)
	}
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}

	var wg sync.WaitGroup
	for range min(PartConcurrency, len(j.ops)) {
		wg.Go(func() {
			var buf []byte
			for i := range pending {
				op := j.ops[i]
				n := int(op.Length) // bounded by MaxPartBytes above
				if cap(buf) < n {
					buf = make([]byte, n)
				}
				part := buf[:n]
				if m, err := j.file.ReadAt(part, op.Offset); m != n {
					fail(fmt.Errorf("upload operation %d: read file: %w", i, err))
					continue
				}
				if err := j.putPart(ctx, i, op, part); err != nil {
					fail(err)
					continue
				}
				mu.Lock()
				done += op.Length
				var err error
				if j.onDone != nil {
					err = j.onDone(i)
				}
				if j.progress != nil {
					j.progress(done, j.size)
				}
				mu.Unlock()
				if err != nil {
					fail(fmt.Errorf("upload operation %d: record progress: %w", i, err))
				}
			}
		})
	}
feed:
	for i := range j.ops {
		if j.skip[i] {
			continue
		}
		select {
		case pending <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(pending)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upload parts: %w", err)
	}
	return nil
}

// putPart sends one part, retrying transport failures and 408/429/5xx.
func (j partsJob) putPart(ctx context.Context, i int, op Operation, part []byte) error {
	method := strings.ToUpper(op.Method)
	header := partHeader(op)
	var last error
	for attempt := 0; attempt <= PartRetries; attempt++ {
		if attempt > 0 {
			if err := j.clock.Sleep(ctx, backoff(attempt, last)); err != nil {
				return fmt.Errorf("upload operation %d: %w (last error: %w)", i, err, last)
			}
		}
		resp, err := j.fetch.Fetch(ctx, method, op.URL, header, bytes.NewReader(part), partResponseCap)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return fmt.Errorf("upload operation %d: %w", i, ctx.Err())
			}
			last = fmt.Errorf("upload operation %d (bytes %d+%d): %w", i, op.Offset, op.Length, err)
			continue
		case resp.Status >= 200 && resp.Status < 300:
			return nil
		}
		last = &partStatusError{index: i, status: resp.Status, header: resp.Header, body: resp.Body}
		if !retryableStatus(resp.Status) {
			return last
		}
	}
	return fmt.Errorf("upload operation %d failed after %d attempts: %w", i, PartRetries+1, last)
}

func retryableStatus(s int) bool {
	return s == http.StatusRequestTimeout || s == http.StatusTooManyRequests || s >= 500
}

// partStatusError is a non-2xx answer from the upload host.
type partStatusError struct {
	index  int
	status int
	header http.Header
	body   []byte
}

func (e *partStatusError) Error() string {
	msg := fmt.Sprintf("upload operation %d: upload host returned %d", e.index, e.status)
	if b := strings.TrimSpace(string(e.body)); b != "" {
		if len(b) > maxPartErrorBody {
			b = b[:maxPartErrorBody] + "…"
		}
		msg += ": " + b
	}
	return msg
}

// backoff returns the wait before retry attempt (1-based): Retry-After in
// seconds when the host sent one, else 500 ms doubled per attempt, both
// capped at 30 s.
func backoff(attempt int, last error) time.Duration {
	var se *partStatusError
	if errors.As(last, &se) && se.header != nil {
		if s, err := strconv.Atoi(strings.TrimSpace(se.header.Get("Retry-After"))); err == nil && s >= 0 {
			return min(time.Duration(s)*time.Second, maxPartBackoff)
		}
	}
	return min(partBackoff<<(attempt-1), maxPartBackoff)
}
