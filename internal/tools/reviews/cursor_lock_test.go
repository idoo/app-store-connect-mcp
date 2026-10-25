package reviews

import (
	"sync"
	"testing"
	"time"
)

// Concurrent mark_seen calls never leave an older cursor stored (HCS-3).
func TestAdvanceCursorConcurrentKeepsNewest(t *testing.T) {
	k := listKit(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts := base.Add(time.Duration(i) * time.Hour)
			if _, err := advanceCursor(k.Env, "a1", ts, ts.Format(time.RFC3339)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	want := base.Add(31 * time.Hour).Format(time.RFC3339)
	if cur, _ := k.State.Cursor("test", "a1", cursorName); cur != want {
		t.Fatalf("cursor = %q, want %q", cur, want)
	}
}
