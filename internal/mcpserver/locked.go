package mcpserver

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// toolsetsName is the name of the session-built toolsets tool.
const toolsetsName = "toolsets"

// Locked reasons of a step naming a tool hidden in this session.
const (
	lockedWrites    = "writes_disabled"
	lockedToolset   = "toolset_disabled"
	lockedStateless = "stateless_fixed"
)

// unlockWrites is the call that makes write tools available.
var unlockWrites = tool.NextStep{Tool: toolsetsName, Args: map[string]any{"action": "enable_writes"}}

// unlockToolset is the call that turns toolset ts on.
func unlockToolset(ts tool.Toolset) tool.NextStep {
	return tool.NextStep{Tool: toolsetsName, Args: map[string]any{"action": "enable", "toolsets": []string{string(ts)}}}
}

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

// lockedTools returns, for every tool hidden in this session but available
// after enabling its toolset and/or writes, the marker its steps carry:
// toolset_disabled (unlock: enable the toolset) before writes_disabled
// (unlock: enable_writes). On stateless HTTP neither can change at runtime,
// so the marker is stateless_fixed with a restart hint and no unlock.
func (s *session) lockedTools() map[string]tool.NextStep {
	p := s.profile()
	w := p
	w.AllowWrites = true
	s.mu.Lock()
	on := maps.Clone(s.on)
	s.mu.Unlock()
	out := map[string]tool.NextStep{}
	for _, t := range s.tools {
		ts := t.Meta().Toolset
		var m tool.NextStep
		switch {
		case !t.ListedIn(w):
			continue
		case s.deps.Stateless && (!on[ts] || !t.ListedIn(p)):
			m = tool.NextStep{Locked: lockedStateless, Hint: errStatelessFixed.Hint}
		case !on[ts]:
			u := unlockToolset(ts)
			m = tool.NextStep{Locked: lockedToolset, Unlock: &u}
		case !t.ListedIn(p):
			u := unlockWrites
			m = tool.NextStep{Locked: lockedWrites, Unlock: &u}
		default:
			continue
		}
		out[t.Name()] = m
	}
	return out
}

// markLocked rewrites follow-up steps (Result.Next, "next" arrays and
// "fix_via" objects in the data) that name a tool hidden in this session:
// they gain locked plus the unlock call (or, on stateless HTTP, a hint).
func (s *session) markLocked(r *tool.Result) {
	s.unifyWritesHint(r)
	locked := s.lockedTools()
	if len(locked) == 0 {
		return
	}
	for i := range r.Next {
		if m, ok := locked[r.Next[i].Tool]; ok && r.Next[i].Locked == "" {
			r.Next[i].Locked, r.Next[i].Unlock, r.Next[i].Hint = m.Locked, m.Unlock, m.Hint
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
// are pinned off, how to unpin them; on stateless HTTP, how to restart).
// Clients without elicitation get restart steps from enable_writes itself.
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
	if s.deps.Stateless {
		b.Hint = errStatelessFixed.Hint
		r.Next = slices.DeleteFunc(r.Next, func(n tool.NextStep) bool { return n.Tool == toolsetsName })
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
func markValue(v any, locked map[string]tool.NextStep) bool {
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

func markStep(v any, locked map[string]tool.NextStep) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	name, _ := m["tool"].(string)
	l, hidden := locked[name]
	if !hidden || m["locked"] != nil {
		return false
	}
	m["locked"] = l.Locked
	if l.Unlock != nil {
		m["unlock"] = map[string]any{"tool": l.Unlock.Tool, "args": l.Unlock.Args}
	}
	if l.Hint != "" {
		m["hint"] = l.Hint
	}
	return true
}
