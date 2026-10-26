package mcpserver

import (
	"bytes"
	"encoding/json"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// toolsetsName is the name of the session-built toolsets tool.
const toolsetsName = "toolsets"

// lockedWrites is the Locked reason of a step naming a tool hidden because
// writes are disabled.
const lockedWrites = "writes_disabled"

// unlockWrites is the call that makes write tools available.
var unlockWrites = tool.NextStep{Tool: toolsetsName, Args: map[string]any{"action": "enable_writes"}}

// writeLocked returns the tools of this session hidden only because writes
// are disabled (nil in write mode).
func (s *session) writeLocked() map[string]bool {
	p := s.profile()
	if p.AllowWrites {
		return nil
	}
	w := p
	w.AllowWrites = true
	out := map[string]bool{}
	for _, t := range s.tools {
		if !t.ListedIn(p) && t.ListedIn(w) {
			out[t.Name()] = true
		}
	}
	return out
}

// markLocked rewrites follow-up steps (Result.Next, "next" arrays and
// "fix_via" objects in the data) that name a tool hidden by read-only mode:
// they gain locked:"writes_disabled" and the enable_writes unlock call.
func (s *session) markLocked(r *tool.Result) {
	s.unifyWritesHint(r)
	locked := s.writeLocked()
	if len(locked) == 0 {
		return
	}
	for i := range r.Next {
		if locked[r.Next[i].Tool] && r.Next[i].Locked == "" {
			u := unlockWrites
			r.Next[i].Locked, r.Next[i].Unlock = lockedWrites, &u
		}
	}
	if r.Data == nil {
		return
	}
	b, err := json.Marshal(r.Data)
	if err != nil || (!bytes.Contains(b, []byte(`"fix_via"`)) && !bytes.Contains(b, []byte(`"next"`))) {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return
	}
	if markValue(v, locked) {
		r.Data = v
	}
}

// writesHint is the one way app-store-connect-mcp tells an agent to turn writes on.
const writesHint = `call toolsets with action "enable_writes"; it asks the user to approve writes for this session`

// unifyWritesHint rewrites a writes_disabled error, whatever tool raised it,
// to the single unlock path: the toolsets enable_writes call (or, when writes
// are pinned off, how to unpin them). Clients without elicitation get restart
// steps from enable_writes itself.
func (s *session) unifyWritesHint(r *tool.Result) {
	if !r.IsError {
		return
	}
	var b *tool.ErrorBody
	switch v := r.Data.(type) {
	case tool.ErrorBody:
		b = &v
		defer func() { r.Data = *b }()
	case *tool.ErrorBody:
		b = v
	}
	if b == nil || b.Error != tool.ErrWritesDisabled.Code {
		return
	}
	if s.deps.WritesLocked {
		b.Hint = errWritesLocked.Hint
		return
	}
	b.Hint = writesHint
	for _, n := range r.Next {
		if n.Tool == toolsetsName {
			return
		}
	}
	r.Next = append(r.Next, unlockWrites)
}

// markValue walks v and marks steps under "fix_via" keys and in "next"
// arrays; it reports whether anything changed.
func markValue(v any, locked map[string]bool) bool {
	changed := false
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			switch k {
			case "fix_via":
				changed = markStep(child, locked) || changed
			case "next":
				if arr, ok := child.([]any); ok {
					for _, step := range arr {
						changed = markStep(step, locked) || changed
					}
				}
			}
			changed = markValue(child, locked) || changed
		}
	case []any:
		for _, child := range x {
			changed = markValue(child, locked) || changed
		}
	}
	return changed
}

func markStep(v any, locked map[string]bool) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	name, _ := m["tool"].(string)
	if !locked[name] || m["locked"] != nil {
		return false
	}
	m["locked"] = lockedWrites
	m["unlock"] = map[string]any{"tool": unlockWrites.Tool, "args": map[string]any{"action": "enable_writes"}}
	return true
}
