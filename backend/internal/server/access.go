package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// AccessFrom returns the access resolved by withAccess. Without one nothing
// is allowed.
func AccessFrom(ctx context.Context) *authz.Access {
	acc, _ := ctx.Value(accessKey).(*authz.Access)
	return acc
}

// withAccess resolves the user's access once per request. A policy that
// cannot be loaded or is invalid grants nothing to non-admins.
func (a *api) withAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFrom(r.Context())
		acc, err := a.deps.Authz.For(r.Context(), id)
		if err != nil {
			a.log.Warn("access policy unusable, only admins have access", "error", err, "user", id.Username)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey, acc)))
	})
}

func notFoundOf(resource string) error {
	switch resource {
	case authz.ResourceConnections:
		return connections.ErrNotFound
	case authz.ResourceRegistries:
		return registries.ErrNotFound
	default:
		return streams.ErrNotFound
	}
}

func singular(resource string) string {
	switch resource {
	case authz.ResourceConnections:
		return "connection"
	case authz.ResourceRegistries:
		return "registry"
	default:
		return "stream"
	}
}

// allow answers 404 when the user may not see the named item and 403 when
// the user sees it but may not perform the action.
func (a *api) allow(w http.ResponseWriter, r *http.Request, resource, action, name string) bool {
	acc := AccessFrom(r.Context())
	if !acc.Can(resource, authz.ActionView, name) {
		a.writeErr(w, notFoundOf(resource))
		return false
	}
	if !acc.Can(resource, action, name) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("you may not %s %s %q", action, singular(resource), name))
		return false
	}
	return true
}

// allowNew checks the action on a name that may not exist yet.
func allowNew(w http.ResponseWriter, r *http.Request, resource, action, name string) bool {
	if !AccessFrom(r.Context()).Can(resource, action, name) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("you may not %s %s %q", action, singular(resource), name))
		return false
	}
	return true
}

// requireAny guards endpoints that need the action on at least some names,
// before the request body is read.
func requireAny(resource, action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !AccessFrom(r.Context()).CanAny(resource, action) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("you may not %s %s", action, resource))
			return
		}
		next(w, r)
	}
}

func (a *api) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !AccessFrom(r.Context()).IsAdmin() {
			writeError(w, http.StatusForbidden, "only Zea admins can manage the access policy")
			return
		}
		next(w, r)
	}
}

// policyResponse is the body of GET and PUT /policy.
type policyResponse struct {
	Policy   authz.Policy         `json:"policy"`
	YAML     string               `json:"yaml"`
	Version  string               `json:"version"`
	Exists   bool                 `json:"exists"`
	Editable bool                 `json:"editable"`
	Error    string               `json:"error,omitempty"`
	Admins   adminsView           `json:"admins"`
	Resource []authz.ResourceInfo `json:"resources"`
}

type adminsView struct {
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

func (a *api) policyResponseOf(doc *authz.Document) policyResponse {
	raw, _ := authz.FormatPolicy(doc.Policy)
	users, groups := a.deps.Authz.Admins()
	return policyResponse{
		Policy:   doc.Policy,
		YAML:     raw,
		Version:  doc.Version,
		Exists:   doc.Exists,
		Editable: doc.Editable,
		Error:    doc.Error,
		Admins:   adminsView{Users: users, Groups: groups},
		Resource: authz.Resources(),
	}
}

func (a *api) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	doc, err := a.deps.Authz.Policy(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.policyResponseOf(doc))
}

type policyInput struct {
	Policy  authz.Policy `json:"policy"`
	Version string       `json:"version"`
}

// maxPolicyBodyBytes caps policy bodies.
const maxPolicyBodyBytes = 1 << 20

func (a *api) handleSavePolicy(w http.ResponseWriter, r *http.Request) {
	var in policyInput
	if !decodeJSONMax(w, r, &in, maxPolicyBodyBytes) {
		return
	}
	doc, err := a.deps.Authz.SavePolicy(r.Context(), in.Policy, in.Version)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("access policy saved", "roles", len(doc.Policy.Roles), "bindings", len(doc.Policy.Bindings),
		"user", IdentityFrom(r.Context()).Username)
	writeJSON(w, http.StatusOK, a.policyResponseOf(doc))
}

// validatePolicyInput carries either YAML (import) or a policy (editor).
type validatePolicyInput struct {
	YAML   *string       `json:"yaml"`
	Policy *authz.Policy `json:"policy"`
}

func (a *api) handleValidatePolicy(w http.ResponseWriter, r *http.Request) {
	var in validatePolicyInput
	if !decodeJSONMax(w, r, &in, maxPolicyBodyBytes) {
		return
	}
	var p authz.Policy
	switch {
	case in.YAML != nil:
		parsed, err := authz.ParsePolicy(*in.YAML)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		p = parsed
	case in.Policy != nil:
		p = *in.Policy
		p.Normalize()
	default:
		writeError(w, http.StatusBadRequest, "send yaml or policy")
		return
	}
	problems := p.Problems()
	if problems == nil {
		problems = []authz.Problem{}
	}
	raw, _ := authz.FormatPolicy(p)
	writeJSON(w, http.StatusOK, map[string]any{"policy": p, "problems": problems, "yaml": raw})
}

type evaluateInput struct {
	Policy authz.Policy `json:"policy"`
	User   string       `json:"user"`
	Groups []string     `json:"groups"`
}

// accessItem is one existing item with the actions a user has on it.
type accessItem struct {
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// handleEvaluatePolicy shows what a draft policy grants a user on the
// items that exist now.
func (a *api) handleEvaluatePolicy(w http.ResponseWriter, r *http.Request) {
	var in evaluateInput
	if !decodeJSONMax(w, r, &in, maxPolicyBodyBytes) {
		return
	}
	in.Policy.Normalize()
	if problems := in.Policy.Problems(); len(problems) > 0 {
		a.writeErr(w, &authz.ValidationError{Problems: problems})
		return
	}
	groups := []string{}
	for _, g := range in.Groups {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	id := &argocd.Identity{Username: strings.TrimSpace(in.User), Groups: groups}
	acc := a.deps.Authz.ForPolicy(in.Policy, id)

	names := map[string][]string{}
	conns, err := a.deps.Store.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	for _, c := range conns {
		names[authz.ResourceConnections] = append(names[authz.ResourceConnections], c.Name)
	}
	if a.deps.Streams != nil {
		all, err := a.deps.Streams.List(r.Context())
		if err != nil {
			a.writeErr(w, err)
			return
		}
		for _, st := range all {
			names[authz.ResourceStreams] = append(names[authz.ResourceStreams], st.Name)
		}
	}
	if a.deps.Registries != nil {
		all, err := a.deps.Registries.List(r.Context())
		if err != nil {
			a.writeErr(w, err)
			return
		}
		for _, reg := range all {
			names[authz.ResourceRegistries] = append(names[authz.ResourceRegistries], reg.Name)
		}
	}
	items := map[string][]accessItem{}
	for _, res := range authz.Resources() {
		list := []accessItem{}
		sorted := names[res.Name]
		sort.Strings(sorted)
		for _, n := range sorted {
			if acts := acc.Actions(res.Name, n); len(acts) > 0 {
				list = append(list, accessItem{Name: n, Actions: acts})
			}
		}
		items[res.Name] = list
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"isAdmin": acc.IsAdmin(),
		"roles":   acc.Roles(),
		"items":   items,
	})
}

// MigrateLegacyAccess turns the deprecated allowedGroups of Connections into
// the access policy when no policy exists yet.
func MigrateLegacyAccess(ctx context.Context, deps Deps, store authz.PolicyStore) (bool, error) {
	conns, err := deps.Store.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list connections: %w", err)
	}
	lc := make([]authz.LegacyConnection, 0, len(conns))
	for _, c := range conns {
		lc = append(lc, authz.LegacyConnection{Name: c.Name, Groups: c.AllowedGroups})
	}
	var ls []authz.LegacyStream
	if deps.Streams != nil {
		all, err := deps.Streams.List(ctx)
		if err != nil {
			return false, fmt.Errorf("list streams: %w", err)
		}
		for _, st := range all {
			ls = append(ls, authz.LegacyStream{Name: st.Name, Connections: st.Connections()})
		}
	}
	return authz.Migrate(ctx, store, lc, ls)
}

// isPolicyErr reports errors of the policy store.
func isPolicyErr(err error) (int, bool) {
	var ve *authz.ValidationError
	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest, true
	case errors.Is(err, authz.ErrReadOnly), errors.Is(err, authz.ErrConflict):
		return http.StatusConflict, true
	}
	return 0, false
}
