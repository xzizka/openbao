// SPDX-License-Identifier: MPL-2.0

package credentialset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/helper/credentialset"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathListSets(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "credential-sets/?$",

		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: operationPrefix,
			OperationSuffix: "sets",
		},

		Fields: map[string]*framework.FieldSchema{
			"after": {
				Type:        framework.TypeString,
				Description: "Optional entry to begin listing after, not required to exist.",
			},
			"limit": {
				Type:        framework.TypeInt,
				Description: "Optional number of entries to return; defaults to all entries.",
			},
		},

		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ListOperation: &framework.PathOperation{
				Callback: b.pathSetList,
				Summary:  "List credential sets.",
			},
		},

		HelpSynopsis:    "List credential sets.",
		HelpDescription: "List the names of all configured credential sets.",
	}
}

func pathSet(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "credential-sets/" + framework.GenericNameRegex("name"),

		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: operationPrefix,
			OperationSuffix: "set",
		},

		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "Name of the credential set.",
			},
			"slots": {
				Type: framework.TypeCommaStringSlice,
				Description: `Names of the credential slots (at least 2). The name of a slot is
the username of its credential.`,
			},
			"validity": {
				Type: framework.TypeDurationSecond,
				Description: `How long one credential stays valid, from its creation until the end of
its overlap.`,
			},
			"overlap": {
				Type: framework.TypeDurationSecond,
				Description: `How long the previous credential stays valid after the next slot became
active. Must cover the time consumers need to pick up the new credential.`,
			},
			"prepare_lead": {
				Type: framework.TypeDurationSecond,
				Description: `How long before it becomes active the next credential is created.
Defaults to 0.`,
			},
			"anchor": {
				Type: framework.TypeTime,
				Description: `Time of the first switch, RFC 3339. Later switches happen at multiples
of the active time after it. Defaults to the creation time.`,
			},
			"password_length": {
				Type:        framework.TypeInt,
				Default:     defaultPasswordLength,
				Description: "Length of generated passwords.",
			},
		},

		ExistenceCheck: b.pathSetExistenceCheck,

		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{
				Callback: b.pathSetWrite,
				Summary:  "Create a credential set.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathSetWrite,
				Summary:  "Credential sets are immutable; delete and recreate to change one.",
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathSetRead,
				Summary:  "Read the configuration of a credential set.",
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: b.pathSetDelete,
				Summary:  "Delete a credential set and retire its credentials.",
			},
		},

		HelpSynopsis:    "Manage a credential set.",
		HelpDescription: pathSetHelp,
	}
}

func pathCreds(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "credential-sets/" + framework.GenericNameRegex("name") + "/creds$",

		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: operationPrefix,
			OperationVerb:   "read",
			OperationSuffix: "credentials",
		},

		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "Name of the credential set.",
			},
		},

		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathCredsRead,
				Summary:  "Read the currently active credential of the set.",
			},
		},

		HelpSynopsis:    "Read the active credential of a credential set.",
		HelpDescription: pathCredsHelp,
	}
}

func pathStatus(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "credential-sets/" + framework.GenericNameRegex("name") + "/status$",

		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: operationPrefix,
			OperationVerb:   "read",
			OperationSuffix: "status",
		},

		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "Name of the credential set.",
			},
		},

		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathStatusRead,
				Summary:  "Read the state of every slot without credentials.",
			},
		},

		HelpSynopsis:    "Read the state of a credential set.",
		HelpDescription: "Returns the state and timing of every slot. No credentials are returned.",
	}
}

func pathRotate(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "credential-sets/" + framework.GenericNameRegex("name") + "/rotate$",

		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: operationPrefix,
			OperationVerb:   "rotate",
			OperationSuffix: "set",
		},

		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "Name of the credential set.",
			},
		},

		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathRotateUpdate,
				Summary:  "Switch to the next slot now.",
			},
		},

		HelpSynopsis:    "Switch a credential set to the next slot now.",
		HelpDescription: pathRotateHelp,
	}
}

func (b *backend) pathSetExistenceCheck(ctx context.Context, req *logical.Request, d *framework.FieldData) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, d.Get("name").(string))
	return e != nil, err
}

func (b *backend) pathSetList(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	after := d.Get("after").(string)
	limit := d.Get("limit").(int)
	if limit <= 0 {
		limit = -1
	}

	entries, err := req.Storage.ListPage(ctx, setsPrefix, after, limit)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(entries), nil
}

func (b *backend) pathSetWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)

	if req.Operation == logical.UpdateOperation {
		return logical.ErrorResponse("credential set %q already exists; credential sets are immutable, delete and recreate it to change it", name), nil
	}

	slots, ok := d.GetOk("slots")
	if !ok {
		return logical.ErrorResponse("slots is required"), nil
	}
	validity, ok := d.GetOk("validity")
	if !ok {
		return logical.ErrorResponse("validity is required"), nil
	}

	now := b.now()
	cfg := credentialset.Config{
		Slots:       slots.([]string),
		Validity:    time.Duration(validity.(int)) * time.Second,
		Overlap:     time.Duration(d.Get("overlap").(int)) * time.Second,
		PrepareLead: time.Duration(d.Get("prepare_lead").(int)) * time.Second,
		Anchor:      now,
	}
	if raw, ok := d.GetOk("anchor"); ok {
		cfg.Anchor = raw.(time.Time)
	}
	cfg.Anchor = cfg.Anchor.UTC()

	if err := cfg.Validate(); err != nil {
		return logical.ErrorResponse("%s", err.Error()), nil
	}

	length := d.Get("password_length").(int)
	if length < minPasswordLength || length > maxPasswordLength {
		return logical.ErrorResponse("password_length must be between %d and %d", minPasswordLength, maxPasswordLength), nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	e := &setEntry{
		Config:         cfg,
		PasswordLength: length,
		Created:        now.UTC(),
		Slots:          credentialset.NewStates(len(cfg.Slots)),
	}

	// Create the first credentials right away so the set is usable
	// immediately; a failure here means the set is not stored.
	if _, err := cfg.Reconcile(ctx, e.Slots, e.rotator(), now); err != nil {
		return nil, fmt.Errorf("failed to create initial credentials: %w", err)
	}
	if err := b.saveSet(ctx, req.Storage, name, e); err != nil {
		return nil, err
	}

	return b.configResponse(e), nil
}

func (b *backend) pathSetRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, d.Get("name").(string))
	if err != nil || e == nil {
		return nil, err
	}
	return b.configResponse(e), nil
}

func (b *backend) pathSetDelete(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)

	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, name)
	if err != nil || e == nil {
		return nil, err
	}

	var errs []error
	for i, st := range e.Slots {
		if st.Credential == nil {
			continue
		}
		if err := e.rotator().Retire(ctx, e.Config.Slots[i]); err != nil {
			errs = append(errs, fmt.Errorf("retiring slot %q: %w", e.Config.Slots[i], err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return nil, req.Storage.Delete(ctx, setsPrefix+name)
}

func (b *backend) pathCredsRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)

	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, name)
	if err != nil || e == nil {
		return nil, err
	}

	if err := b.reconcile(ctx, req.Storage, name, e); err != nil {
		if errors.Is(err, logical.ErrReadOnly) {
			return nil, err
		}
		// A failing target system must not take the consumers down: serve
		// what is still valid and let the status endpoint show the error.
		b.Logger().Error("credential set reconcile failed", "set", name, "error", err)
	}

	now := b.now()
	sel, err := e.Config.Select(e.Slots, now)
	switch {
	case errors.Is(err, credentialset.ErrNotActive):
		return logical.ErrorResponse("credential set %q becomes active at %s", name, e.Config.Anchor.Format(time.RFC3339)), nil
	case err != nil:
		return nil, fmt.Errorf("credential set %q: %w", name, err)
	}

	st := e.Slots[sel.Index]
	data := make(map[string]any, len(st.Credential)+6)
	for k, v := range st.Credential {
		data[k] = v
	}
	data["slot"] = sel.Slot
	data["rotated_at"] = formatTime(st.RotatedAt)
	data["active_since"] = formatTime(sel.ActiveSince)
	data["active_until"] = formatTime(sel.ActiveUntil)
	data["valid_until"] = formatTime(sel.ValidUntil)
	data["degraded"] = sel.Degraded

	// No lease: the consumer pulls the credential periodically and the
	// validity is bounded by the schedule, not by a lease.
	return &logical.Response{Data: data}, nil
}

func (b *backend) pathStatusRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, d.Get("name").(string))
	if err != nil || e == nil {
		return nil, err
	}

	now := b.now()
	infos := e.Config.Describe(e.Slots, now)
	slots := make([]map[string]any, 0, len(infos))
	for _, in := range infos {
		m := map[string]any{
			"name":  in.Name,
			"state": in.State,
			"cycle": in.Cycle,
		}
		if in.Cycle >= 0 {
			m["rotated_at"] = formatTime(in.RotatedAt)
			m["active_since"] = formatTime(in.ActiveSince)
			m["active_until"] = formatTime(in.ActiveUntil)
			m["valid_until"] = formatTime(in.ValidUntil)
		}
		if in.LastError != "" {
			m["last_error"] = in.LastError
			m["last_error_at"] = formatTime(in.LastErrorAt)
		}
		slots = append(slots, m)
	}

	resp := b.configResponse(e)
	resp.Data["now"] = formatTime(now)
	resp.Data["slot_states"] = slots
	resp.Data["pending_work"] = e.Config.Pending(e.Slots, now)

	if sel, err := e.Config.Select(e.Slots, now); err == nil {
		resp.Data["active_slot"] = sel.Slot
		resp.Data["degraded"] = sel.Degraded
	}
	return resp, nil
}

func (b *backend) pathRotateUpdate(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)

	b.mu.Lock()
	defer b.mu.Unlock()

	e, err := b.loadSet(ctx, req.Storage, name)
	if err != nil || e == nil {
		return nil, err
	}

	now := b.now()
	e.Config = e.Config.Advance(now)
	if err := b.reconcile(ctx, req.Storage, name, e); err != nil {
		if errors.Is(err, logical.ErrReadOnly) {
			return nil, err
		}
		return logical.ErrorResponse("rotation finished with errors: %s", err.Error()), nil
	}

	sel, err := e.Config.Select(e.Slots, now)
	if err != nil {
		return nil, err
	}
	resp := b.configResponse(e)
	resp.Data["active_slot"] = sel.Slot
	resp.Data["active_since"] = formatTime(sel.ActiveSince)
	resp.Data["active_until"] = formatTime(sel.ActiveUntil)
	resp.Data["valid_until"] = formatTime(sel.ValidUntil)
	return resp, nil
}

func (b *backend) configResponse(e *setEntry) *logical.Response {
	c := e.Config
	return &logical.Response{Data: map[string]any{
		"slots":           c.Slots,
		"validity":        int64(c.Validity / time.Second),
		"overlap":         int64(c.Overlap / time.Second),
		"prepare_lead":    int64(c.PrepareLead / time.Second),
		"active_time":     int64(c.ActiveTime() / time.Second),
		"rotation_period": int64(c.RotationPeriod() / time.Second),
		"anchor":          formatTime(c.Anchor),
		"password_length": e.PasswordLength,
		"created":         formatTime(e.Created),
	}}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

const pathSetHelp = `
A credential set consists of N slots that are rotated in alternation.

  validity       V  how long one credential stays valid
  overlap        O  how long the previous credential stays valid after a switch
  prepare_lead   L  how long before a switch the next credential is created
  active time    S = V - O - L, how long each slot is the active one

Switches happen every S, starting at the anchor. A credential is created L
before its slot becomes active and ends O after the next slot took over.
Credential sets are immutable; delete and recreate one to change it.
`

const pathCredsHelp = `
Returns the credential of the currently active slot together with its
validity window. The response carries no lease. If the active slot could not be
rotated, the still-valid credential of the previous slot is returned and
"degraded" is true.
`

const pathRotateHelp = `
Makes the next slot active immediately and shifts the schedule so that later
switches follow from this moment. The previous credential stays valid for the
configured overlap.
`
