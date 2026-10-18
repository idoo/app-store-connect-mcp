package insights

import (
	"cmp"
	"slices"
	"time"
)

// digestStateKind is the state-store kind of digest records; the key is
// digestStateKey. They are independent of the reviews_list and
// testflight_feedback_list cursors.
const digestStateKind = "digest"

// digestWatermark is how far one source has been seen: the timestamp of the
// last returned item and every returned id carrying exactly that timestamp,
// so ties at the boundary are neither repeated nor lost.
type digestWatermark struct {
	TS  string   `json:"ts"`
	IDs []string `json:"ids,omitempty"`
}

// digestState is the persisted digest record of one profile+app.
type digestState struct {
	Sources map[string]digestWatermark `json:"sources,omitempty"`
	// Snapshot maps "type/id" to the last reported state of that object.
	Snapshot  map[string]string `json:"snapshot,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// digestKeyed is one candidate item reduced to what the watermark needs.
// Index points back into the caller's slice.
type digestKeyed struct {
	ID    string
	TS    time.Time
	Index int
}

func digestStateKey(profile, appID string) string { return profile + "__" + appID }

// digestParseTime parses an Apple timestamp; unparsable values are zero.
func digestParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// digestStart returns the stored watermark of name, or (fresh) one at base
// so a first digest never presents older history as new.
func digestStart(st digestState, name string, base time.Time) (digestWatermark, bool) {
	if w, ok := st.Sources[name]; ok && w.TS != "" {
		return w, false
	}
	return digestWatermark{TS: base.UTC().Format(time.RFC3339Nano)}, true
}

// time returns the watermark instant and whether one is set.
func (w digestWatermark) time() (time.Time, bool) {
	if w.TS == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, w.TS)
	return t, err == nil
}

// digestSeen reports whether an item at ts with id is at or behind w.
func digestSeen(w digestWatermark, id string, ts time.Time) bool {
	wt, ok := w.time()
	if !ok {
		return false
	}
	if ts.Before(wt) {
		return true
	}
	return ts.Equal(wt) && slices.Contains(w.IDs, id)
}

// digestSelect returns the unseen entries oldest first (ties by id), at most
// limit of them, and whether more unseen entries remain.
func digestSelect(entries []digestKeyed, w digestWatermark, limit int) ([]digestKeyed, bool) {
	unseen := make([]digestKeyed, 0, len(entries))
	for _, e := range entries {
		if !digestSeen(w, e.ID, e.TS) {
			unseen = append(unseen, e)
		}
	}
	slices.SortStableFunc(unseen, func(a, b digestKeyed) int {
		if c := a.TS.Compare(b.TS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if len(unseen) > limit {
		return unseen[:limit], true
	}
	return unseen, false
}

// digestAdvance moves w to the last of returned (oldest first). Ids already
// recorded at the same instant are kept, so a tie split across calls is
// returned exactly once. Nothing returned leaves w unchanged.
func digestAdvance(w digestWatermark, returned []digestKeyed) digestWatermark {
	if len(returned) == 0 {
		return w
	}
	last := returned[len(returned)-1].TS
	var ids []string
	if wt, ok := w.time(); ok && wt.Equal(last) {
		ids = append(ids, w.IDs...)
	}
	for _, r := range returned {
		if r.TS.Equal(last) && !slices.Contains(ids, r.ID) {
			ids = append(ids, r.ID)
		}
	}
	slices.Sort(ids)
	return digestWatermark{TS: last.UTC().Format(time.RFC3339Nano), IDs: ids}
}
