package authz

import (
	"path"
	"slices"
	"sort"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
)

// Access is what one user may do, resolved from the policy. A nil Access
// allows nothing.
type Access struct {
	admin bool
	roles []string
	rules []Rule
}

// AdminAccess allows everything.
func AdminAccess() *Access { return &Access{admin: true} }

// Evaluate resolves the roles and rules a policy grants to a user.
func Evaluate(p Policy, id *argocd.Identity) *Access {
	acc := &Access{roles: []string{}}
	if id == nil {
		return acc
	}
	bound := map[string]bool{}
	for _, b := range p.Bindings {
		if subjectMatches(b, id) {
			for _, r := range b.Roles {
				bound[r] = true
			}
		}
	}
	for _, r := range p.Roles {
		if bound[r.Name] && !slices.Contains(acc.roles, r.Name) {
			acc.roles = append(acc.roles, r.Name)
			acc.rules = append(acc.rules, r.Rules...)
		}
	}
	sort.Strings(acc.roles)
	return acc
}

func subjectMatches(b Binding, id *argocd.Identity) bool {
	switch {
	case b.User != "":
		return b.User == id.Username || (id.UserID != "" && b.User == id.UserID)
	case b.Group == Everyone:
		return true
	case b.Group != "":
		return slices.Contains(id.Groups, b.Group)
	}
	return false
}

// IsAdmin reports whether the user is a Zea admin from the chart values.
func (x *Access) IsAdmin() bool { return x != nil && x.admin }

// Roles are the policy roles bound to the user.
func (x *Access) Roles() []string {
	if x == nil {
		return []string{}
	}
	return slices.Clone(x.roles)
}

// Can reports whether the user may perform action on the named resource.
// Any granted action also grants view.
func (x *Access) Can(resource, action, name string) bool {
	if x == nil {
		return false
	}
	if x.admin {
		return true
	}
	for _, r := range x.rules {
		if r.Resource == resource && grants(r, action) && matches(r.Pattern, name) {
			return true
		}
	}
	return false
}

// CanAny reports whether the user may perform action on at least some
// resource names, for example to offer a "New" button.
func (x *Access) CanAny(resource, action string) bool {
	if x == nil {
		return false
	}
	if x.admin {
		return true
	}
	for _, r := range x.rules {
		if r.Resource == resource && grants(r, action) {
			return true
		}
	}
	return false
}

// Actions lists the actions the user may perform on the named resource.
func (x *Access) Actions(resource, name string) []string {
	out := []string{}
	for _, a := range actionsOf(resource) {
		if x.Can(resource, a, name) {
			out = append(out, a)
		}
	}
	return out
}

// Summary maps every resource type to the actions the user may perform on
// at least some of its names.
func (x *Access) Summary() map[string][]string {
	out := map[string][]string{}
	for _, res := range resources {
		acts := []string{}
		for _, a := range res.Actions {
			if x.CanAny(res.Name, a) {
				acts = append(acts, a)
			}
		}
		out[res.Name] = acts
	}
	return out
}

func grants(r Rule, action string) bool {
	if action == ActionView {
		return len(r.Actions) > 0
	}
	return slices.Contains(r.Actions, action)
}

func matches(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}
