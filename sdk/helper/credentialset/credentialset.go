// SPDX-License-Identifier: MPL-2.0

// Package credentialset implements the scheduling and state machine of a
// "credential set": N credential slots that are rotated in alternation and
// presented to consumers as one logical credential.
//
// The package is deliberately free of storage and clock access. All functions
// take the current time as an argument and the only side effects are calls
// to a Rotator supplied by the caller (usually a secrets engine).
//
// Timing model
//
//	V  validity of one credential
//	O  overlap: how long the previous credential stays valid after a switch
//	L  prepare lead: how long before a switch the next credential is created
//	S  active time of one slot, S = V - O - L
//
// Switch k happens at anchor + k*S and activates slot k mod N. The credential
// for switch k is created at anchor + k*S - L and ends (is retired) at
// anchor + (k+1)*S + O.
package credentialset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotActive is returned by Select before the first switch (anchor).
	ErrNotActive = errors.New("credential set is not active yet")
	// ErrNoCredential is returned by Select when no slot holds a valid
	// credential.
	ErrNoCredential = errors.New("no valid credential is available")
)

// Config describes a credential set. It is immutable after creation.
type Config struct {
	Slots       []string      `json:"slots"`
	Validity    time.Duration `json:"validity"`
	Overlap     time.Duration `json:"overlap"`
	PrepareLead time.Duration `json:"prepare_lead"`
	Anchor      time.Time     `json:"anchor"`
}

// ActiveTime returns S, the time each slot is the active one.
func (c Config) ActiveTime() time.Duration {
	return c.Validity - c.Overlap - c.PrepareLead
}

// RotationPeriod returns P = N*S, the time between two rotations of one slot.
func (c Config) RotationPeriod() time.Duration {
	return time.Duration(len(c.Slots)) * c.ActiveTime()
}

// Validate checks the configuration and the timing constraints.
func (c Config) Validate() error {
	n := len(c.Slots)
	if n < 2 {
		return fmt.Errorf("at least 2 slots are required, got %d", n)
	}

	seen := make(map[string]struct{}, n)
	for _, s := range c.Slots {
		if strings.TrimSpace(s) == "" {
			return errors.New("slot names must not be empty")
		}
		if _, dup := seen[s]; dup {
			return fmt.Errorf("duplicate slot name %q", s)
		}
		seen[s] = struct{}{}
	}

	if c.Validity <= 0 {
		return errors.New("validity must be positive")
	}
	if c.Overlap < 0 {
		return errors.New("overlap must not be negative")
	}
	if c.PrepareLead < 0 {
		return errors.New("prepare_lead must not be negative")
	}
	if c.Anchor.IsZero() {
		return errors.New("anchor must be set")
	}

	s := c.ActiveTime()
	if s < time.Second {
		return fmt.Errorf("validity must exceed overlap + prepare_lead by at least 1s (active time would be %s)", s)
	}
	if c.PrepareLead >= s {
		return fmt.Errorf("prepare_lead (%s) must be shorter than the active time (%s)", c.PrepareLead, s)
	}
	// A slot is retired at (k+1)*S+O and prepared again at (k+N)*S-L; the
	// preparation must not happen before the retirement, otherwise the
	// credential would be replaced while it is still inside its overlap.
	if time.Duration(n-1)*s < c.Overlap+c.PrepareLead {
		return fmt.Errorf(
			"a slot would be rotated again before its overlap ends: (slots-1) * active time (%s) must be at least overlap + prepare_lead (%s); use more slots or a shorter overlap",
			time.Duration(n-1)*s, c.Overlap+c.PrepareLead,
		)
	}
	return nil
}

// SlotState is the persisted state of one slot.
type SlotState struct {
	// Cycle is the switch number the credential was created for, -1 if the
	// slot never had a credential.
	Cycle int64 `json:"cycle"`
	// Credential is nil when the slot is retired or pending.
	Credential map[string]string `json:"credential,omitempty"`
	RotatedAt  time.Time         `json:"rotated_at"`

	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

// NewStates returns the initial state for n slots.
func NewStates(n int) []SlotState {
	st := make([]SlotState, n)
	for i := range st {
		st[i].Cycle = -1
	}
	return st
}

// Rotator performs the work on the target system. For a secrets engine that
// owns the credential itself it only generates a new value.
type Rotator interface {
	// Rotate creates a new credential for the slot and returns its fields,
	// for example "username" and "password".
	Rotate(ctx context.Context, slot string) (map[string]string, error)
	// Retire ends the validity of the slot's current credential.
	Retire(ctx context.Context, slot string) error
}

func floorDiv(a, b time.Duration) int64 {
	q := int64(a / b)
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func posMod(a int64, n int) int {
	m := int(a % int64(n))
	if m < 0 {
		m += n
	}
	return m
}

func (c Config) cycleStart(k int64) time.Time {
	return c.Anchor.Add(time.Duration(k) * c.ActiveTime())
}

// activeCycle returns the cycle that is active at now; it is negative before
// the anchor.
func (c Config) activeCycle(now time.Time) int64 {
	return floorDiv(now.Sub(c.Anchor), c.ActiveTime())
}

// graceEnd is the time the credential of cycle k stops being valid.
func (c Config) graceEnd(k int64) time.Time {
	return c.cycleStart(k + 1).Add(c.Overlap)
}

type step struct {
	slot  int
	cycle int64
}

// plan computes the rotations and retirements needed at now.
func (c Config) plan(states []SlotState, now time.Time) (rotate []step, retire []int) {
	n := len(c.Slots)
	s := c.ActiveTime()

	// K is the newest cycle whose preparation time has been reached.
	big := floorDiv(now.Sub(c.Anchor)+c.PrepareLead, s)
	ka := c.activeCycle(now)

	for i := 0; i < n; i++ {
		ki := big - int64(posMod(big-int64(i), n))
		if ki < 0 || ki < ka {
			continue
		}
		if states[i].Credential == nil || states[i].Cycle < ki {
			rotate = append(rotate, step{slot: i, cycle: ki})
		}
	}

	activeOK := false
	if ka >= 0 {
		st := states[posMod(ka, n)]
		activeOK = st.Credential != nil && st.Cycle == ka
	}

	for i := 0; i < n; i++ {
		st := states[i]
		if st.Credential == nil || st.Cycle >= ka {
			continue
		}
		if now.Before(c.graceEnd(st.Cycle)) {
			continue
		}
		// Fail safe: never retire the last working credential while the
		// replacement is missing.
		if !activeOK {
			continue
		}
		retire = append(retire, i)
	}
	return rotate, retire
}

// Pending reports whether Reconcile would call the Rotator at now.
func (c Config) Pending(states []SlotState, now time.Time) bool {
	r, t := c.plan(states, now)
	return len(r) > 0 || len(t) > 0
}

// Reconcile brings the slot states in line with the schedule at now. It is
// idempotent and catches up after any downtime: only the newest cycle of each
// slot is ever created.
//
// Reconcile mutates states in place. It returns changed=true if anything in
// states was modified (including recorded errors), and a joined error for
// the slots whose rotation or retirement failed.
func (c Config) Reconcile(ctx context.Context, states []SlotState, r Rotator, now time.Time) (bool, error) {
	if len(states) != len(c.Slots) {
		return false, fmt.Errorf("state has %d slots, configuration has %d", len(states), len(c.Slots))
	}

	rotate, _ := c.plan(states, now)
	changed := false
	var errs []error

	for _, st := range rotate {
		name := c.Slots[st.slot]
		cred, err := r.Rotate(ctx, name)
		if err != nil {
			states[st.slot].LastError = err.Error()
			states[st.slot].LastErrorAt = now
			changed = true
			errs = append(errs, fmt.Errorf("rotating slot %q: %w", name, err))
			continue
		}
		states[st.slot] = SlotState{
			Cycle:      st.cycle,
			Credential: cred,
			RotatedAt:  now,
		}
		changed = true
	}

	// Retirement is planned after the rotations: a failed rotation of the
	// active slot must keep the previous credential alive.
	_, retire := c.plan(states, now)
	for _, i := range retire {
		name := c.Slots[i]
		if err := r.Retire(ctx, name); err != nil {
			states[i].LastError = err.Error()
			states[i].LastErrorAt = now
			changed = true
			errs = append(errs, fmt.Errorf("retiring slot %q: %w", name, err))
			continue
		}
		states[i].Credential = nil
		states[i].LastError = ""
		changed = true
	}

	return changed, errors.Join(errs...)
}

// Selection describes the credential to serve.
type Selection struct {
	Index       int
	Slot        string
	Cycle       int64
	ActiveSince time.Time
	ActiveUntil time.Time
	ValidUntil  time.Time
	// Degraded is true when the slot that should be active has no credential
	// and an older still-valid one is served instead.
	Degraded bool
}

// Select picks the credential to serve at now.
func (c Config) Select(states []SlotState, now time.Time) (Selection, error) {
	if len(states) != len(c.Slots) {
		return Selection{}, fmt.Errorf("state has %d slots, configuration has %d", len(states), len(c.Slots))
	}
	if now.Before(c.Anchor) {
		return Selection{}, ErrNotActive
	}

	n := len(c.Slots)
	ka := c.activeCycle(now)
	idx := posMod(ka, n)

	sel := Selection{Index: idx}
	if st := states[idx]; st.Credential != nil && st.Cycle == ka {
		sel.Cycle = ka
	} else {
		best := -1
		for i, st := range states {
			if st.Credential == nil || st.Cycle > ka {
				continue
			}
			if best < 0 || st.Cycle > states[best].Cycle {
				best = i
			}
		}
		if best < 0 {
			return Selection{}, ErrNoCredential
		}
		sel.Index = best
		sel.Cycle = states[best].Cycle
		sel.Degraded = true
	}

	sel.Slot = c.Slots[sel.Index]
	sel.ActiveSince = c.cycleStart(sel.Cycle)
	sel.ActiveUntil = c.cycleStart(sel.Cycle + 1)
	sel.ValidUntil = c.graceEnd(sel.Cycle)
	return sel, nil
}

// SlotInfo is a secret-free view of one slot for status output.
type SlotInfo struct {
	Name        string
	State       string
	Cycle       int64
	RotatedAt   time.Time
	ActiveSince time.Time
	ActiveUntil time.Time
	ValidUntil  time.Time
	LastError   string
	LastErrorAt time.Time
}

// Slot states reported by Describe.
const (
	StatePending  = "pending"  // never had a credential
	StatePrepared = "prepared" // created, will become active
	StateActive   = "active"
	StateGrace    = "grace"   // no longer active, still valid (overlap)
	StateExpired  = "expired" // overlap over, retirement outstanding
	StateRetired  = "retired" // credential ended
)

// Describe returns the state of every slot at now.
func (c Config) Describe(states []SlotState, now time.Time) []SlotInfo {
	ka := c.activeCycle(now)
	out := make([]SlotInfo, 0, len(c.Slots))
	for i, name := range c.Slots {
		st := states[i]
		info := SlotInfo{
			Name:        name,
			Cycle:       st.Cycle,
			RotatedAt:   st.RotatedAt,
			LastError:   st.LastError,
			LastErrorAt: st.LastErrorAt,
		}
		switch {
		case st.Credential == nil && st.Cycle < 0:
			info.State = StatePending
		case st.Credential == nil:
			info.State = StateRetired
		case st.Cycle > ka:
			info.State = StatePrepared
		case st.Cycle == ka:
			info.State = StateActive
		case now.Before(c.graceEnd(st.Cycle)):
			info.State = StateGrace
		default:
			info.State = StateExpired
		}
		if st.Cycle >= 0 {
			info.ActiveSince = c.cycleStart(st.Cycle)
			info.ActiveUntil = c.cycleStart(st.Cycle + 1)
			info.ValidUntil = c.graceEnd(st.Cycle)
		}
		out = append(out, info)
	}
	return out
}

// Advance returns a configuration whose anchor is shifted so that the next
// switch happens at now. The set must be reconciled afterwards.
func (c Config) Advance(now time.Time) Config {
	s := c.ActiveTime()
	ka := c.activeCycle(now)
	if ka < -1 {
		ka = -1
	}
	c.Anchor = now.Add(-time.Duration(ka+1) * s)
	return c
}
