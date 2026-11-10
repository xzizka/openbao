// SPDX-License-Identifier: MPL-2.0

// Package credentialset is a secrets engine that serves one logical credential
// out of N alternating credential slots.
//
// The engine owns the credentials itself: a slot is a username (its name) and
// a generated password. The scheduling and the overlap handling live in
// sdk/helper/credentialset and are independent of how a slot is rotated, so
// other engines can reuse them with a Rotator that talks to a target system.
package credentialset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-secure-stdlib/base62"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/helper/credentialset"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	operationPrefix = "credential-sets"
	setsPrefix      = "sets/"

	defaultPasswordLength = 32
	minPasswordLength     = 16
	maxPasswordLength     = 256
)

func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := Backend()
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

func Backend() *backend {
	b := &backend{now: time.Now}
	b.Backend = &framework.Backend{
		Help: strings.TrimSpace(backendHelp),

		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{setsPrefix},
		},

		Paths: []*framework.Path{
			pathListSets(b),
			pathSet(b),
			pathCreds(b),
			pathStatus(b),
			pathRotate(b),
		},

		PeriodicFunc: b.periodic,
		BackendType:  logical.TypeLogical,
	}
	return b
}

type backend struct {
	*framework.Backend

	// mu serializes all reads and writes of set entries. Sets are few and
	// operations are short, so one lock keeps the reasoning simple.
	mu sync.Mutex

	// now is replaceable in tests.
	now func() time.Time
}

// setEntry is the single storage entry of a credential set: configuration
// and slot state are stored together so they can never diverge.
type setEntry struct {
	Config         credentialset.Config      `json:"config"`
	PasswordLength int                       `json:"password_length"`
	Created        time.Time                 `json:"created"`
	Slots          []credentialset.SlotState `json:"slots"`
}

func (e *setEntry) rotator() credentialset.Rotator {
	return passwordRotator{length: e.PasswordLength}
}

// passwordRotator is the Rotator of this engine: the engine owns the
// credential, so rotating a slot means generating a new password.
type passwordRotator struct {
	length int
}

func (r passwordRotator) Rotate(_ context.Context, slot string) (map[string]string, error) {
	pw, err := base62.Random(r.length)
	if err != nil {
		return nil, err
	}
	return map[string]string{"username": slot, "password": pw}, nil
}

// Retire has nothing to do on a target system; dropping the stored value is
// done by the helper.
func (r passwordRotator) Retire(context.Context, string) error { return nil }

func (b *backend) loadSet(ctx context.Context, s logical.Storage, name string) (*setEntry, error) {
	raw, err := s.Get(ctx, setsPrefix+name)
	if err != nil || raw == nil {
		return nil, err
	}
	var e setEntry
	if err := json.Unmarshal(raw.Value, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (b *backend) saveSet(ctx context.Context, s logical.Storage, name string, e *setEntry) error {
	entry, err := logical.StorageEntryJSON(setsPrefix+name, e)
	if err != nil {
		return err
	}
	return s.Put(ctx, entry)
}

// reconcile performs the rotations that are due and persists the result.
// The caller must hold b.mu.
//
// On a node that cannot write it returns logical.ErrReadOnly without touching
// the target system so the request is forwarded to the active node.
func (b *backend) reconcile(ctx context.Context, s logical.Storage, name string, e *setEntry) error {
	now := b.now()
	if !e.Config.Pending(e.Slots, now) {
		return nil
	}
	if b.System().ReplicationState().HasState(consts.ReplicationDRSecondary | consts.ReplicationPerformanceStandby) {
		return logical.ErrReadOnly
	}

	changed, rerr := e.Config.Reconcile(ctx, e.Slots, e.rotator(), now)
	if changed {
		if err := b.saveSet(ctx, s, name, e); err != nil {
			return errors.Join(err, rerr)
		}
	}
	return rerr
}

// periodic reconciles every set so retirements happen even when nobody reads
// the credentials.
func (b *backend) periodic(ctx context.Context, req *logical.Request) error {
	names, err := req.Storage.List(ctx, setsPrefix)
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, name := range names {
		e, err := b.loadSet(ctx, req.Storage, name)
		if err != nil || e == nil {
			if err != nil {
				b.Logger().Error("failed to load credential set", "set", name, "error", err)
			}
			continue
		}
		if err := b.reconcile(ctx, req.Storage, name, e); err != nil {
			b.Logger().Error("credential set reconcile failed", "set", name, "error", err)
		}
	}
	return nil
}

const backendHelp = `
The credential-sets backend serves one logical credential out of N slots that
are rotated in alternation. Each slot is valid for a configurable time and the
previous credential stays valid for a configurable overlap after a switch, so
that consumers have time to pick up the new credential.
`
