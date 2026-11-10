// SPDX-License-Identifier: MPL-2.0

package credentialset

import (
	"context"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type harness struct {
	t       *testing.T
	b       *backend
	storage logical.Storage
	clock   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, clock: t0}

	config := logical.TestBackendConfig()
	config.StorageView = &logical.InmemStorage{}
	h.storage = config.StorageView

	b := Backend()
	b.now = func() time.Time { return h.clock }
	require.NoError(t, b.Setup(context.Background(), config))
	h.b = b
	return h
}

func (h *harness) do(op logical.Operation, path string, data map[string]any) (*logical.Response, error) {
	h.t.Helper()
	return h.b.HandleRequest(context.Background(), &logical.Request{
		Operation: op,
		Path:      path,
		Data:      data,
		Storage:   h.storage,
	})
}

func (h *harness) mustDo(op logical.Operation, path string, data map[string]any) *logical.Response {
	h.t.Helper()
	resp, err := h.do(op, path, data)
	require.NoError(h.t, err)
	if resp != nil {
		require.False(h.t, resp.IsError(), "%v", resp.Error())
	}
	return resp
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *harness) creds(name string) map[string]any {
	h.t.Helper()
	resp := h.mustDo(logical.ReadOperation, "credential-sets/"+name+"/creds", nil)
	require.NotNil(h.t, resp)
	require.Nil(h.t, resp.Secret, "credentials must not carry a lease")
	return resp.Data
}

func (h *harness) states(name string) map[string]string {
	h.t.Helper()
	resp := h.mustDo(logical.ReadOperation, "credential-sets/"+name+"/status", nil)
	out := map[string]string{}
	for _, s := range resp.Data["slot_states"].([]map[string]any) {
		out[s["name"].(string)] = s["state"].(string)
	}
	return out
}

func createOriginal(h *harness) {
	h.mustDo(logical.CreateOperation, "credential-sets/app", map[string]any{
		"slots":    []string{"xx_a", "xx_b"},
		"validity": int((30 * day).Seconds()),
		"overlap":  int((15 * day).Seconds()),
	})
}

func TestOriginalScenario(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)

	first := h.creds("app")
	require.Equal(t, "xx_a", first["username"])
	require.Equal(t, "xx_a", first["slot"])
	require.Len(t, first["password"], defaultPasswordLength)
	require.Equal(t, false, first["degraded"])
	require.Equal(t, "2026-01-01T00:00:00Z", first["active_since"])
	require.Equal(t, "2026-01-16T00:00:00Z", first["active_until"])
	require.Equal(t, "2026-01-31T00:00:00Z", first["valid_until"])

	// Reads between switches return the same credential.
	h.advance(7 * day)
	require.Equal(t, first["password"], h.creds("app")["password"])

	// Day 15: xx_b takes over, xx_a stays valid until day 30.
	h.advance(8 * day)
	second := h.creds("app")
	require.Equal(t, "xx_b", second["username"])
	require.NotEqual(t, first["password"], second["password"])
	require.Equal(t, map[string]string{"xx_a": "grace", "xx_b": "active"}, h.states("app"))

	// Day 30: xx_a is rotated and active again with a new password.
	h.advance(15 * day)
	third := h.creds("app")
	require.Equal(t, "xx_a", third["username"])
	require.NotEqual(t, first["password"], third["password"])
	require.Equal(t, map[string]string{"xx_a": "active", "xx_b": "grace"}, h.states("app"))
}

func TestShortOverlapRetiresPreviousCredential(t *testing.T) {
	h := newHarness(t)
	h.mustDo(logical.CreateOperation, "credential-sets/app", map[string]any{
		"slots":        "xx_a,xx_b",
		"validity":     "30d",
		"overlap":      "1d",
		"prepare_lead": "1h",
	})
	require.Equal(t, map[string]string{"xx_a": "active", "xx_b": "pending"}, h.states("app"))

	// One hour before the switch the next credential exists but is not served.
	active := 29*day - time.Hour
	h.advance(active - time.Hour)
	require.Equal(t, "xx_a", h.creds("app")["username"])
	require.Equal(t, map[string]string{"xx_a": "active", "xx_b": "prepared"}, h.states("app"))

	h.advance(time.Hour + time.Minute)
	require.Equal(t, "xx_b", h.creds("app")["username"])
	require.Equal(t, "grace", h.states("app")["xx_a"])

	// The overlap ends one day after the switch.
	h.advance(day)
	require.Equal(t, "xx_b", h.creds("app")["username"])
	require.Equal(t, "retired", h.states("app")["xx_a"])
}

func TestPeriodicRetiresWithoutReads(t *testing.T) {
	h := newHarness(t)
	h.mustDo(logical.CreateOperation, "credential-sets/app", map[string]any{
		"slots": "a,b", "validity": "10h", "overlap": "1h",
	})

	// Past the end of the overlap of slot a and nobody has read anything.
	h.advance(9*time.Hour + 30*time.Minute) // S = 9h, overlap ends at 10h
	require.NoError(t, h.b.periodic(context.Background(), &logical.Request{Storage: h.storage}))
	require.Equal(t, "grace", h.states("app")["a"])

	h.advance(time.Hour)
	require.NoError(t, h.b.periodic(context.Background(), &logical.Request{Storage: h.storage}))
	require.Equal(t, "retired", h.states("app")["a"])
}

func TestCredentialsSurviveRestart(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)
	want := h.creds("app")

	// A fresh backend instance on the same storage serves the same value.
	config := logical.TestBackendConfig()
	config.StorageView = h.storage
	b2 := Backend()
	b2.now = func() time.Time { return h.clock }
	require.NoError(t, b2.Setup(context.Background(), config))
	h.b = b2

	require.Equal(t, want["password"], h.creds("app")["password"])
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{"missing slots", map[string]any{"validity": "30d"}, "slots is required"},
		{"missing validity", map[string]any{"slots": "a,b"}, "validity is required"},
		{"one slot", map[string]any{"slots": "a", "validity": "30d"}, "at least 2 slots"},
		{"overlap too long", map[string]any{"slots": "a,b", "validity": "30d", "overlap": "20d"}, "before its overlap ends"},
		{"short password", map[string]any{"slots": "a,b", "validity": "30d", "password_length": 4}, "password_length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := h.do(logical.CreateOperation, "credential-sets/bad", tt.data)
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.True(t, resp.IsError())
			require.Contains(t, resp.Error().Error(), tt.want)
		})
	}

	resp, err := h.do(logical.ReadOperation, "credential-sets/bad", nil)
	require.NoError(t, err)
	require.Nil(t, resp, "failed creates must not store anything")
}

func TestSetIsImmutable(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)

	resp, err := h.do(logical.UpdateOperation, "credential-sets/app", map[string]any{
		"slots": "a,b", "validity": "10d",
	})
	require.NoError(t, err)
	require.True(t, resp.IsError())
	require.Contains(t, resp.Error().Error(), "immutable")
}

func TestAnchorInTheFuture(t *testing.T) {
	h := newHarness(t)
	h.mustDo(logical.CreateOperation, "credential-sets/app", map[string]any{
		"slots": "a,b", "validity": "10h", "overlap": "1h",
		"anchor": t0.Add(2 * time.Hour).Format(time.RFC3339),
	})

	resp, err := h.do(logical.ReadOperation, "credential-sets/app/creds", nil)
	require.NoError(t, err)
	require.True(t, resp.IsError())
	require.Contains(t, resp.Error().Error(), "becomes active at 2026-01-01T02:00:00Z")

	h.advance(2 * time.Hour)
	require.Equal(t, "a", h.creds("app")["username"])
}

func TestRotateSwitchesNow(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)
	first := h.creds("app")

	h.advance(2 * day)
	resp := h.mustDo(logical.UpdateOperation, "credential-sets/app/rotate", nil)
	require.Equal(t, "xx_b", resp.Data["active_slot"])
	require.Equal(t, "2026-01-03T00:00:00Z", resp.Data["active_since"])
	require.Equal(t, "2026-01-18T00:00:00Z", resp.Data["active_until"])

	second := h.creds("app")
	require.Equal(t, "xx_b", second["username"])
	require.Equal(t, map[string]string{"xx_a": "grace", "xx_b": "active"}, h.states("app"))
	require.NotEqual(t, first["password"], second["password"])

	// The schedule follows from the manual switch: next switch is 15 days later.
	h.advance(15 * day)
	require.Equal(t, "xx_a", h.creds("app")["username"])
}

func TestStatusHasNoSecrets(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)
	pw := h.creds("app")["password"].(string)

	resp := h.mustDo(logical.ReadOperation, "credential-sets/app/status", nil)
	require.NotContains(t, toString(resp.Data), pw)
	require.Equal(t, "xx_a", resp.Data["active_slot"])
	require.EqualValues(t, 15*24*3600, resp.Data["active_time"])
	require.EqualValues(t, 30*24*3600, resp.Data["rotation_period"])

	resp = h.mustDo(logical.ReadOperation, "credential-sets/app", nil)
	require.NotContains(t, toString(resp.Data), pw)
}

func TestListAndDelete(t *testing.T) {
	h := newHarness(t)
	createOriginal(h)
	h.mustDo(logical.CreateOperation, "credential-sets/other", map[string]any{"slots": "a,b", "validity": "10h", "overlap": "1h"})

	resp := h.mustDo(logical.ListOperation, "credential-sets/", nil)
	require.Equal(t, []string{"app", "other"}, resp.Data["keys"])

	h.mustDo(logical.DeleteOperation, "credential-sets/app", nil)

	resp = h.mustDo(logical.ListOperation, "credential-sets/", nil)
	require.Equal(t, []string{"other"}, resp.Data["keys"])

	r, err := h.do(logical.ReadOperation, "credential-sets/app/creds", nil)
	require.NoError(t, err)
	require.Nil(t, r)
}

func TestUnknownSet(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"credential-sets/nope", "credential-sets/nope/creds", "credential-sets/nope/status"} {
		resp, err := h.do(logical.ReadOperation, p, nil)
		require.NoError(t, err)
		require.Nil(t, resp, p)
	}
}

func toString(m map[string]any) string {
	var s string
	for k, v := range m {
		s += k + "=" + stringify(v) + ";"
	}
	return s
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []map[string]any:
		var s string
		for _, e := range t {
			s += toString(e)
		}
		return s
	case []string:
		var s string
		for _, e := range t {
			s += e + ","
		}
		return s
	default:
		return ""
	}
}
