// SPDX-License-Identifier: MPL-2.0

package credentialset

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type fakeRotator struct {
	rotations map[string]int
	retired   map[string]int
	failRot   map[string]bool
	failRet   map[string]bool
}

func newFake() *fakeRotator {
	return &fakeRotator{
		rotations: map[string]int{},
		retired:   map[string]int{},
		failRot:   map[string]bool{},
		failRet:   map[string]bool{},
	}
}

func (f *fakeRotator) Rotate(_ context.Context, slot string) (map[string]string, error) {
	if f.failRot[slot] {
		return nil, errors.New("target system unavailable")
	}
	f.rotations[slot]++
	return map[string]string{
		"username": slot,
		"password": fmt.Sprintf("pw-%s-%d", slot, f.rotations[slot]),
	}, nil
}

func (f *fakeRotator) Retire(_ context.Context, slot string) error {
	if f.failRet[slot] {
		return errors.New("retire failed")
	}
	f.retired[slot]++
	return nil
}

func dual(validity, overlap, lead time.Duration) Config {
	return Config{
		Slots:       []string{"xx_a", "xx_b"},
		Validity:    validity,
		Overlap:     overlap,
		PrepareLead: lead,
		Anchor:      t0,
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"original scenario 2x30d overlap 15d", dual(30*day, 15*day, 0), ""},
		{"short overlap with lead", dual(30*day, day, time.Hour), ""},
		{"three slots", Config{Slots: []string{"a", "b", "c"}, Validity: 30 * day, Overlap: 2 * time.Hour, PrepareLead: time.Hour, Anchor: t0}, ""},
		{"overlap too long for two slots", dual(30*day, 20*day, 0), "rotated again before its overlap ends"},
		{"single slot", Config{Slots: []string{"a"}, Validity: day, Anchor: t0}, "at least 2 slots"},
		{"duplicate slot", Config{Slots: []string{"a", "a"}, Validity: day, Anchor: t0}, "duplicate"},
		{"empty slot name", Config{Slots: []string{"a", " "}, Validity: day, Anchor: t0}, "must not be empty"},
		{"zero validity", dual(0, 0, 0), "validity must be positive"},
		{"negative overlap", dual(day, -time.Hour, 0), "must not be negative"},
		{"no active time", dual(day, day, 0), "at least 1s"},
		{"lead longer than active", dual(10*time.Hour, 0, 9*time.Hour+30*time.Minute), "shorter than the active time"},
		{"no anchor", Config{Slots: []string{"a", "b"}, Validity: day}, "anchor must be set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestDerivedTimes(t *testing.T) {
	cfg := dual(30*day, 15*day, 0)
	require.Equal(t, 15*day, cfg.ActiveTime())
	require.Equal(t, 30*day, cfg.RotationPeriod())

	cfg = dual(30*day, day, time.Hour)
	require.Equal(t, 29*day-time.Hour, cfg.ActiveTime())
}

// TestOriginalScenario walks the 90-day table from the original requirement:
// two accounts, 30 day validity, alternating every 15 days.
func TestOriginalScenario(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, 15*day, 0)
	require.NoError(t, cfg.Validate())

	states := NewStates(2)
	rot := newFake()

	wantSlot := func(d int) string {
		if (d/15)%2 == 0 {
			return "xx_a"
		}
		return "xx_b"
	}

	passwords := map[string]map[string]bool{"xx_a": {}, "xx_b": {}}
	for d := 0; d < 90; d++ {
		now := t0.Add(time.Duration(d)*day + time.Hour)
		_, err := cfg.Reconcile(ctx, states, rot, now)
		require.NoError(t, err)

		sel, err := cfg.Select(states, now)
		require.NoError(t, err)
		require.Equal(t, wantSlot(d), sel.Slot, "day %d", d)
		require.False(t, sel.Degraded)
		require.True(t, now.Before(sel.ValidUntil))

		passwords[sel.Slot][states[sel.Index].Credential["password"]] = true

		// During the overlap the outgoing credential must still be valid.
		if d >= 15 {
			prev := (sel.Index + 1) % 2
			require.NotNil(t, states[prev].Credential, "day %d: previous slot must be in overlap", d)
			require.Equal(t, sel.Cycle-1, states[prev].Cycle)
		}
	}

	// Each slot is rotated once per 30 days: 3 activations in 90 days each
	// for xx_a (days 0, 30, 60) and xx_b (days 15, 45, 75).
	require.Equal(t, 3, rot.rotations["xx_a"])
	require.Equal(t, 3, rot.rotations["xx_b"])
	require.Len(t, passwords["xx_a"], 3)
	require.Len(t, passwords["xx_b"], 3)
}

func TestShortOverlapRetiresOutgoingSlot(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, day, time.Hour)
	require.NoError(t, cfg.Validate())
	s := cfg.ActiveTime()

	states := NewStates(2)
	rot := newFake()

	// Before the anchor nothing is active but slot 0 is prepared one hour
	// ahead.
	_, err := cfg.Reconcile(ctx, states, rot, t0.Add(-30*time.Minute))
	require.NoError(t, err)
	_, err = cfg.Select(states, t0.Add(-30*time.Minute))
	require.ErrorIs(t, err, ErrNotActive)
	require.Equal(t, 1, rot.rotations["xx_a"])
	require.Equal(t, 0, rot.rotations["xx_b"])

	// Just before the first switch the next slot is prepared.
	now := t0.Add(s - 30*time.Minute)
	_, err = cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	require.Equal(t, 1, rot.rotations["xx_b"])
	infos := cfg.Describe(states, now)
	require.Equal(t, StateActive, infos[0].State)
	require.Equal(t, StatePrepared, infos[1].State)

	// After the switch the old credential stays valid for the overlap only.
	now = t0.Add(s + time.Hour)
	_, err = cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	sel, err := cfg.Select(states, now)
	require.NoError(t, err)
	require.Equal(t, "xx_b", sel.Slot)
	require.NotNil(t, states[0].Credential)
	require.Equal(t, StateGrace, cfg.Describe(states, now)[0].State)
	require.Equal(t, t0.Add(s+day), cfg.Describe(states, now)[0].ValidUntil)

	now = t0.Add(s + day + time.Minute)
	changed, err := cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, states[0].Credential)
	require.Equal(t, 1, rot.retired["xx_a"])
	require.Equal(t, StateRetired, cfg.Describe(states, now)[0].State)

	// The retired slot is prepared again one hour before its next switch.
	next := t0.Add(2*s - 30*time.Minute)
	_, err = cfg.Reconcile(ctx, states, rot, next)
	require.NoError(t, err)
	require.Equal(t, 2, rot.rotations["xx_a"])
}

func TestReconcileIsIdempotent(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, 15*day, 0)
	states := NewStates(2)
	rot := newFake()

	now := t0.Add(time.Hour)
	changed, err := cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, cfg.Pending(states, now))

	changed, err = cfg.Reconcile(ctx, states, rot, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, 1, rot.rotations["xx_a"])
}

func TestCatchUpAfterDowntime(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, day, time.Hour)
	s := cfg.ActiveTime()
	states := NewStates(2)
	rot := newFake()

	// Set was created long ago and the node was down for 10 cycles.
	now := t0.Add(10*s + 2*time.Hour)
	_, err := cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)

	sel, err := cfg.Select(states, now)
	require.NoError(t, err)
	require.Equal(t, int64(10), sel.Cycle)
	require.Equal(t, "xx_a", sel.Slot)

	// Only the credential for the active cycle was created; stale cycles are
	// not replayed.
	require.Equal(t, 1, rot.rotations["xx_a"])
	require.Equal(t, 0, rot.rotations["xx_b"])
	require.Nil(t, states[1].Credential)
	require.Equal(t, StatePending, cfg.Describe(states, now)[1].State)
}

func TestFailedRotationKeepsPreviousCredential(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, day, 0)
	s := cfg.ActiveTime()
	states := NewStates(2)
	rot := newFake()

	_, err := cfg.Reconcile(ctx, states, rot, t0.Add(time.Hour))
	require.NoError(t, err)
	_, err = cfg.Reconcile(ctx, states, rot, t0.Add(s-time.Hour))
	require.NoError(t, err)

	// The target system for xx_b is down when the switch is due.
	rot.failRot["xx_b"] = true
	rot.rotations["xx_b"] = 0
	states[1] = SlotState{Cycle: -1} // preparation had not happened

	now := t0.Add(s + 2*time.Hour)
	changed, err := cfg.Reconcile(ctx, states, rot, now)
	require.Error(t, err)
	require.ErrorContains(t, err, `rotating slot "xx_b"`)
	require.True(t, changed)
	require.Contains(t, states[1].LastError, "unavailable")

	// The old credential is still served, flagged as degraded.
	sel, err := cfg.Select(states, now)
	require.NoError(t, err)
	require.True(t, sel.Degraded)
	require.Equal(t, "xx_a", sel.Slot)

	// Even long after the overlap the only working credential is not
	// retired while there is no replacement.
	now = t0.Add(s + 5*day)
	_, err = cfg.Reconcile(ctx, states, rot, now)
	require.Error(t, err)
	require.NotNil(t, states[0].Credential)
	require.Equal(t, 0, rot.retired["xx_a"])
	sel, err = cfg.Select(states, now)
	require.NoError(t, err)
	require.True(t, sel.Degraded)

	// Once the target system recovers the set heals itself.
	rot.failRot["xx_b"] = false
	_, err = cfg.Reconcile(ctx, states, rot, now.Add(time.Minute))
	require.NoError(t, err)
	sel, err = cfg.Select(states, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, sel.Degraded)
	require.Equal(t, "xx_b", sel.Slot)
	require.Nil(t, states[0].Credential)
	require.Equal(t, 1, rot.retired["xx_a"])
}

func TestRetireFailureIsRetried(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, day, 0)
	s := cfg.ActiveTime()
	states := NewStates(2)
	rot := newFake()

	for _, now := range []time.Time{t0.Add(time.Hour), t0.Add(s + time.Hour)} {
		_, err := cfg.Reconcile(ctx, states, rot, now)
		require.NoError(t, err)
	}

	rot.failRet["xx_a"] = true
	now := t0.Add(s + day + time.Hour)
	_, err := cfg.Reconcile(ctx, states, rot, now)
	require.ErrorContains(t, err, `retiring slot "xx_a"`)
	require.NotNil(t, states[0].Credential)

	rot.failRet["xx_a"] = false
	_, err = cfg.Reconcile(ctx, states, rot, now.Add(time.Minute))
	require.NoError(t, err)
	require.Nil(t, states[0].Credential)
}

func TestThreeSlotsDoNotCollide(t *testing.T) {
	ctx := t.Context()
	cfg := Config{
		Slots: []string{"a", "b", "c"}, Validity: 30 * day,
		Overlap: 2 * time.Hour, PrepareLead: time.Hour, Anchor: t0,
	}
	require.NoError(t, cfg.Validate())
	states := NewStates(3)
	rot := newFake()

	for h := 0; h < 24*100; h++ {
		now := t0.Add(time.Duration(h) * time.Hour)
		_, err := cfg.Reconcile(ctx, states, rot, now)
		require.NoError(t, err)
		sel, err := cfg.Select(states, now)
		require.NoError(t, err)
		require.False(t, sel.Degraded)

		valid := 0
		for _, st := range states {
			if st.Credential != nil {
				valid++
			}
		}
		// active + at most one in overlap + at most one prepared
		require.LessOrEqual(t, valid, 3, "hour %d", h)
	}
}

func TestAdvanceSwitchesImmediately(t *testing.T) {
	ctx := t.Context()
	cfg := dual(30*day, day, 0)
	s := cfg.ActiveTime()
	states := NewStates(2)
	rot := newFake()

	now := t0.Add(2 * time.Hour)
	_, err := cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	sel, err := cfg.Select(states, now)
	require.NoError(t, err)
	require.Equal(t, "xx_a", sel.Slot)

	cfg = cfg.Advance(now)
	require.Equal(t, now, cfg.Anchor.Add(time.Duration(1)*s))
	_, err = cfg.Reconcile(ctx, states, rot, now)
	require.NoError(t, err)
	sel, err = cfg.Select(states, now)
	require.NoError(t, err)
	require.Equal(t, "xx_b", sel.Slot)
	require.False(t, sel.Degraded)

	// The previous credential keeps working for the overlap.
	require.Equal(t, StateGrace, cfg.Describe(states, now)[0].State)
}

func TestAdvanceBeforeAnchorActivatesFirstSlot(t *testing.T) {
	cfg := dual(30*day, day, 0)
	cfg.Anchor = t0.Add(10 * day)
	cfg = cfg.Advance(t0)
	require.Equal(t, t0, cfg.Anchor)
}

func TestSelectBeforeAnchor(t *testing.T) {
	cfg := dual(30*day, day, 0)
	_, err := cfg.Select(NewStates(2), t0.Add(-time.Second))
	require.ErrorIs(t, err, ErrNotActive)
}

func TestSelectWithoutCredentials(t *testing.T) {
	cfg := dual(30*day, day, 0)
	_, err := cfg.Select(NewStates(2), t0.Add(time.Hour))
	require.ErrorIs(t, err, ErrNoCredential)
}
