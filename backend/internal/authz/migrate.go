package authz

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// LegacyConnection is a Connection with its deprecated allowedGroups.
type LegacyConnection struct {
	Name   string
	Groups []string
}

// LegacyStream is a Stream with the Connections it uses.
type LegacyStream struct {
	Name        string
	Connections []string
}

var unsafeRoleChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// LegacyPolicy translates allowedGroups into roles that keep today's
// access: each group gets view and run on its Connections, and on every
// Stream whose Connections it may all use. Everyone (*) is a group of its own.
func LegacyPolicy(conns []LegacyConnection, strms []LegacyStream) Policy {
	byGroup := map[string][]string{}
	for _, c := range conns {
		for _, g := range c.Groups {
			if g = strings.TrimSpace(g); g != "" && !slices.Contains(byGroup[g], c.Name) {
				byGroup[g] = append(byGroup[g], c.Name)
			}
		}
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	p := Policy{Roles: []Role{}, Bindings: []Binding{}}
	used := map[string]bool{}
	for _, g := range groups {
		usable := func(name string) bool {
			return slices.Contains(byGroup[g], name) || slices.Contains(byGroup[Everyone], name)
		}
		role := Role{Name: legacyRoleName(g, used), Rules: []Rule{}}
		if g == Everyone {
			role.Description = "Migrated from connections shared with everyone (allowedGroups: *)"
		} else {
			role.Description = fmt.Sprintf("Migrated from connection allowedGroups of group %q", g)
		}
		names := slices.Clone(byGroup[g])
		sort.Strings(names)
		for _, n := range names {
			role.Rules = append(role.Rules, Rule{Resource: ResourceConnections, Pattern: n, Actions: []string{ActionView, ActionRun}})
		}
		for _, st := range strms {
			if len(st.Connections) == 0 || !all(st.Connections, usable) {
				continue
			}
			// Streams of everyone are granted by the everyone role only.
			if g != Everyone && all(st.Connections, func(n string) bool { return slices.Contains(byGroup[Everyone], n) }) {
				continue
			}
			role.Rules = append(role.Rules, Rule{Resource: ResourceStreams, Pattern: st.Name, Actions: []string{ActionView, ActionRun}})
		}
		used[role.Name] = true
		p.Roles = append(p.Roles, role)
		p.Bindings = append(p.Bindings, Binding{Group: g, Roles: []string{role.Name}})
	}
	return p
}

func all(names []string, ok func(string) bool) bool {
	for _, n := range names {
		if !ok(n) {
			return false
		}
	}
	return true
}

func legacyRoleName(group string, used map[string]bool) string {
	base := "everyone"
	if group != Everyone {
		base = strings.Trim(unsafeRoleChars.ReplaceAllString(group, "-"), "-._")
		if base == "" {
			base = "group"
		}
		base = "legacy-" + base
	}
	if len(base) > 56 {
		base = base[:56]
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// Migrate creates the policy from allowedGroups when no policy exists yet.
// It reports whether it created one. Once a policy exists, allowedGroups
// are ignored.
func Migrate(ctx context.Context, store PolicyStore, conns []LegacyConnection, strms []LegacyStream) (bool, error) {
	doc, err := store.Load(ctx)
	if err != nil {
		return false, err
	}
	if doc.Exists {
		return false, nil
	}
	_, err = store.Save(ctx, LegacyPolicy(conns, strms), "")
	if errors.Is(err, ErrConflict) {
		return false, nil
	}
	return err == nil, err
}
