package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// PlanCapacity is the number of pending plans kept per session.
	PlanCapacity = 32
	// PlanTTL is how long a plan stays valid.
	PlanTTL = 15 * time.Minute
)

type plan struct {
	tool     string
	args     string
	fp       string
	expires  time.Time
	sequence uint64
}

// Planner is the in-memory, per-session [tool.Planner]. Plan ids are random
// 128-bit values bound to the tool name, canonical args and state
// fingerprint; plans expire after [PlanTTL], at most [PlanCapacity] are kept
// (oldest evicted first), and each id can be verified only once.
type Planner struct {
	mu    sync.Mutex
	now   func() time.Time
	plans map[string]plan
	seq   uint64
}

var _ tool.Planner = (*Planner)(nil)

// NewPlanner returns an empty Planner; now defaults to time.Now.
func NewPlanner(now func() time.Time) *Planner {
	if now == nil {
		now = time.Now
	}
	return &Planner{now: now, plans: map[string]plan{}}
}

// Propose records a plan and returns its id. An empty fingerprint is refused
// so a plan can never match an unknown state.
func (p *Planner) Propose(_ context.Context, toolName string, args any, fingerprint string, _ tool.PlanPreview) (string, error) {
	if fingerprint == "" {
		return "", fmt.Errorf("%w: plan for %s has no state fingerprint", tool.ErrInternal, toolName)
	}
	h, err := argsHash(args)
	if err != nil {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("%w: plan id: %v", tool.ErrInternal, err)
	}
	id := hex.EncodeToString(b[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.prune(now)
	for len(p.plans) >= PlanCapacity {
		p.evictOldest()
	}
	p.seq++
	p.plans[id] = plan{tool: toolName, args: h, fp: fingerprint, expires: now.Add(PlanTTL), sequence: p.seq}
	return id, nil
}

// Verify checks and consumes planID. Any lookup of a known id consumes it, so
// a mismatching confirm cannot be retried against the same plan.
func (p *Planner) Verify(_ context.Context, planID, toolName string, args any, fingerprint string) error {
	h, err := argsHash(args)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pl, ok := p.plans[planID]
	if !ok {
		return fmt.Errorf("%w: unknown plan id", tool.ErrStalePlan)
	}
	delete(p.plans, planID)
	switch {
	case !p.now().Before(pl.expires):
		return fmt.Errorf("%w: plan expired", tool.ErrStalePlan)
	case pl.tool != toolName:
		return fmt.Errorf("%w: plan was made for %s", tool.ErrStalePlan, pl.tool)
	case pl.args != h:
		return fmt.Errorf("%w: arguments differ from the plan", tool.ErrStalePlan)
	case fingerprint == "" || pl.fp != fingerprint:
		return fmt.Errorf("%w: remote state changed since the plan", tool.ErrStalePlan)
	}
	return nil
}

// Claim consumes the newest unexpired [tool.FlagFingerprint] plan for
// toolName with the same canonical args.
func (p *Planner) Claim(_ context.Context, toolName string, args any) error {
	h, err := argsHash(args)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(p.now())
	var found string
	var seq uint64
	for id, pl := range p.plans {
		if pl.tool == toolName && pl.args == h && pl.fp == tool.FlagFingerprint && (found == "" || pl.sequence > seq) {
			found, seq = id, pl.sequence
		}
	}
	if found == "" {
		return fmt.Errorf("%w: no matching preview for %s", tool.ErrPreviewRequired, toolName)
	}
	delete(p.plans, found)
	return nil
}

func (p *Planner) prune(now time.Time) {
	for id, pl := range p.plans {
		if !now.Before(pl.expires) {
			delete(p.plans, id)
		}
	}
}

func (p *Planner) evictOldest() {
	var oldest string
	var seq uint64
	for id, pl := range p.plans {
		if oldest == "" || pl.sequence < seq {
			oldest, seq = id, pl.sequence
		}
	}
	delete(p.plans, oldest)
}

func argsHash(args any) (string, error) {
	b, err := tool.CanonicalArgs(args)
	if err != nil {
		return "", fmt.Errorf("%w: %v", tool.ErrInternal, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
