// Package authz decides what a user may do inside Zea. Argo CD has already
// enforced "applications get" on the anchor Application and "extensions
// invoke zea"; this package adds Zea-level rules on top: admins from the
// chart values, and a policy of roles bound to groups and users.
package authz

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
)

// cacheTTL bounds how long a policy edited outside Zea (for example by Argo
// CD) takes to apply. Saves through Zea apply at once.
const cacheTTL = 5 * time.Second

// Authorizer evaluates admins and the access policy.
type Authorizer struct {
	adminUsers  map[string]bool
	adminGroups map[string]bool
	store       PolicyStore

	mu       sync.Mutex
	cached   *Document
	loadedAt time.Time
}

// New returns an Authorizer for the given admins and policy store. A nil
// store means an empty policy: only admins have access.
func New(adminUsers, adminGroups []string, store PolicyStore) *Authorizer {
	a := &Authorizer{adminUsers: map[string]bool{}, adminGroups: map[string]bool{}, store: store}
	for _, u := range adminUsers {
		a.adminUsers[u] = true
	}
	for _, g := range adminGroups {
		a.adminGroups[g] = true
	}
	return a
}

// Admins returns the configured admin users and groups.
func (a *Authorizer) Admins() (users, groups []string) {
	users, groups = []string{}, []string{}
	for u := range a.adminUsers {
		users = append(users, u)
	}
	for g := range a.adminGroups {
		groups = append(groups, g)
	}
	sort.Strings(users)
	sort.Strings(groups)
	return users, groups
}

// IsAdmin reports whether the user is a Zea admin. Admins have full access
// and are the only ones who may edit the policy.
func (a *Authorizer) IsAdmin(id *argocd.Identity) bool {
	if id == nil {
		return false
	}
	if id.Username != "" && a.adminUsers[id.Username] {
		return true
	}
	if id.UserID != "" && a.adminUsers[id.UserID] {
		return true
	}
	for _, g := range id.Groups {
		if a.adminGroups[g] {
			return true
		}
	}
	return false
}

// For resolves the access of a user. When the policy cannot be loaded the
// returned Access grants nothing to non-admins, and the error explains why.
func (a *Authorizer) For(ctx context.Context, id *argocd.Identity) (*Access, error) {
	if a.IsAdmin(id) {
		return AdminAccess(), nil
	}
	doc, err := a.current(ctx)
	if err != nil {
		return Evaluate(Policy{}, id), err
	}
	if doc.Error != "" {
		return Evaluate(Policy{}, id), errors.New(doc.Error)
	}
	return Evaluate(doc.Policy, id), nil
}

// ForPolicy resolves the access a draft policy would give a user.
func (a *Authorizer) ForPolicy(p Policy, id *argocd.Identity) *Access {
	if a.IsAdmin(id) {
		return AdminAccess()
	}
	return Evaluate(p, id)
}

func (a *Authorizer) current(ctx context.Context) (*Document, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached != nil && time.Since(a.loadedAt) < cacheTTL {
		return a.cached, nil
	}
	doc, err := a.load(ctx)
	if err != nil {
		return nil, err
	}
	a.cached, a.loadedAt = doc, time.Now()
	return doc, nil
}

func (a *Authorizer) load(ctx context.Context) (*Document, error) {
	if a.store == nil {
		return &Document{Policy: Policy{Roles: []Role{}, Bindings: []Binding{}}}, nil
	}
	return a.store.Load(ctx)
}

// Policy loads the stored policy, bypassing the cache.
func (a *Authorizer) Policy(ctx context.Context) (*Document, error) {
	return a.load(ctx)
}

// ValidationError lists the problems of a policy that was not saved.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	msgs := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		msgs = append(msgs, p.String())
	}
	return "invalid policy: " + strings.Join(msgs, "; ")
}

// SavePolicy validates and stores the policy and applies it at once.
func (a *Authorizer) SavePolicy(ctx context.Context, p Policy, version string) (*Document, error) {
	if a.store == nil {
		return nil, errors.New("the access policy is not configured")
	}
	p.Normalize()
	if problems := p.Problems(); len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	doc, err := a.store.Save(ctx, p, version)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cached, a.loadedAt = doc, time.Now()
	a.mu.Unlock()
	return doc, nil
}
