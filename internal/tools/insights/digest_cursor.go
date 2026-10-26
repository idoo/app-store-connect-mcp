package insights

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

// digestStateKind is the state-store kind of digest records; the key is
// digestStateKey. They are independent of the reviews_list and
// testflight_feedback_list cursors.
const digestStateKind = "digest"

// digestOverlap is how far below the watermark each scan re-reads, so items
// Apple indexes late (createdDate older than items already seen) are still
// returned once; digestSeenMax caps the seen ids kept per source.
const (
	digestOverlap = 72 * time.Hour
	digestSeenMax = 2000
)

// digestWatermark is how far one source has been seen: the newest returned
// createdDate (TS) and every returned id with its createdDate down to
// TS − digestOverlap. Floor, when set, is an instant scans never go below: a
// fresh source's baseline, or what accept_gap or the seen cap gave up.
type digestWatermark struct {
	TS    string               `json:"ts"`
	Seen  map[string]time.Time `json:"seen,omitempty"`
	Floor time.Time            `json:"floor,omitzero"`
	// IDs is the v0.5.0 record (ids at exactly TS, nothing below TS
	// unseen); digestUpgrade folds it into Seen and Floor on load.
	IDs []string `json:"ids,omitempty"`
}

// digestState is the persisted digest record of one profile+app.
type digestState struct {
	Sources map[string]digestWatermark `json:"sources,omitempty"`
	// Snapshot maps "type/id" to the last reported state of that object.
	// Builds stay in it only while their processing state is not terminal.
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

// digestUpgrade converts v0.5.0 watermarks: everything before TS was seen,
// so TS becomes the floor and the tie ids become seen at TS.
func digestUpgrade(st *digestState) {
	for name, w := range st.Sources {
		if w.Seen != nil || !w.Floor.IsZero() || w.TS == "" {
			continue
		}
		wt, ok := w.time()
		if !ok {
			continue
		}
		w.Floor = wt
		if len(w.IDs) > 0 {
			w.Seen = make(map[string]time.Time, len(w.IDs))
			for _, id := range w.IDs {
				w.Seen[id] = wt
			}
		}
		w.IDs = nil
		st.Sources[name] = w
	}
}

// digestStart returns the stored watermark of name, or (fresh) one at base
// with base as its floor, so a first digest never presents older history as
// new.
func digestStart(st digestState, name string, base time.Time) (digestWatermark, bool) {
	if w, ok := st.Sources[name]; ok && w.TS != "" {
		return w, false
	}
	b := base.UTC()
	return digestWatermark{TS: b.Format(time.RFC3339Nano), Floor: b}, true
}

// time returns the watermark instant and whether one is set.
func (w digestWatermark) time() (time.Time, bool) {
	if w.TS == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, w.TS)
	return t, err == nil
}

// floor is the oldest instant a scan reads: TS − digestOverlap, raised to
// Floor. Without a watermark it is zero (read everything).
func (w digestWatermark) floor() time.Time {
	wt, ok := w.time()
	if !ok {
		return w.Floor
	}
	f := wt.Add(-digestOverlap)
	if w.Floor.After(f) {
		return w.Floor
	}
	return f
}

// digestSeen reports whether id was already returned.
func digestSeen(w digestWatermark, id string) bool {
	_, ok := w.Seen[id]
	return ok
}

// digestSelect returns the unseen entries oldest first (ties by id), at most
// limit of them, and whether more unseen entries remain.
func digestSelect(entries []digestKeyed, w digestWatermark, limit int) ([]digestKeyed, bool) {
	unseen := make([]digestKeyed, 0, len(entries))
	for _, e := range entries {
		if !digestSeen(w, e.ID) {
			unseen = append(unseen, e)
		}
	}
	slices.SortStableFunc(unseen, digestKeyedCmp)
	if len(unseen) > limit {
		return unseen[:limit], true
	}
	return unseen, false
}

func digestKeyedCmp(a, b digestKeyed) int {
	if c := a.TS.Compare(b.TS); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// digestAdvance records returned as seen, moves TS to the newest returned
// createdDate (never back) and prunes. Nothing returned leaves w unchanged.
func digestAdvance(w digestWatermark, returned []digestKeyed) digestWatermark {
	if len(returned) == 0 {
		return w
	}
	wt, _ := w.time()
	seen := make(map[string]time.Time, len(w.Seen)+len(returned))
	maps.Copy(seen, w.Seen)
	for _, r := range returned {
		seen[r.ID] = r.TS
		if r.TS.After(wt) {
			wt = r.TS
		}
	}
	out := digestWatermark{TS: wt.UTC().Format(time.RFC3339Nano), Seen: seen, Floor: w.Floor}
	out.prune()
	return out
}

// prune drops seen ids older than TS − digestOverlap and, past
// digestSeenMax, the oldest ones; the floor rises above any id dropped by
// the cap so it is not returned again. A floor below the overlap is cleared.
func (w *digestWatermark) prune() {
	wt, ok := w.time()
	if !ok {
		return
	}
	cut := wt.Add(-digestOverlap)
	for id, ts := range w.Seen {
		if ts.Before(cut) {
			delete(w.Seen, id)
		}
	}
	if n := len(w.Seen) - digestSeenMax; n > 0 {
		all := make([]digestKeyed, 0, len(w.Seen))
		for id, ts := range w.Seen {
			all = append(all, digestKeyed{ID: id, TS: ts})
		}
		slices.SortFunc(all, digestKeyedCmp)
		for _, k := range all[:n] {
			delete(w.Seen, k.ID)
		}
		if f := all[n-1].TS.Add(time.Nanosecond); f.After(w.Floor) {
			w.Floor = f
		}
	}
	if !w.Floor.After(cut) {
		w.Floor = time.Time{}
	}
}
