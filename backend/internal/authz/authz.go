// Package authz decides what a user may do inside Zea. Argo CD has already
// enforced "applications get" on the anchor Application and "extensions
// invoke zea"; this package adds Zea-level rules on top.
package authz

import (
	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

// Authorizer evaluates admin and Connection access rules.
type Authorizer struct {
	adminUsers  map[string]bool
	adminGroups map[string]bool
}

// New returns an Authorizer for the given admin users and groups.
func New(adminUsers, adminGroups []string) *Authorizer {
	a := &Authorizer{adminUsers: map[string]bool{}, adminGroups: map[string]bool{}}
	for _, u := range adminUsers {
		a.adminUsers[u] = true
	}
	for _, g := range adminGroups {
		a.adminGroups[g] = true
	}
	return a
}

// IsAdmin reports whether the user may manage Connections.
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

// CanUse reports whether the user may see and run a Connection.
func (a *Authorizer) CanUse(id *argocd.Identity, c *connections.Connection) bool {
	if a.IsAdmin(id) {
		return true
	}
	if id == nil || c == nil {
		return false
	}
	for _, allowed := range c.AllowedGroups {
		if allowed == connections.AllGroups {
			return true
		}
		for _, g := range id.Groups {
			if g == allowed {
				return true
			}
		}
	}
	return false
}
