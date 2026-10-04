package authz

import (
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

func TestAuthorizer(t *testing.T) {
	a := New([]string{"admin"}, []string{"platform"})
	conn := &connections.Connection{AllowedGroups: []string{"devs"}}
	open := &connections.Connection{AllowedGroups: []string{"*"}}
	closed := &connections.Connection{}

	cases := []struct {
		id                      *argocd.Identity
		admin, conn, open, none bool
	}{
		{&argocd.Identity{Username: "admin"}, true, true, true, true},
		{&argocd.Identity{Username: "x", Groups: []string{"platform"}}, true, true, true, true},
		{&argocd.Identity{Username: "dev", Groups: []string{"devs"}}, false, true, true, false},
		{&argocd.Identity{Username: "guest"}, false, false, true, false},
		{nil, false, false, false, false},
	}
	for i, c := range cases {
		if got := a.IsAdmin(c.id); got != c.admin {
			t.Errorf("case %d IsAdmin = %v", i, got)
		}
		if got := a.CanUse(c.id, conn); got != c.conn {
			t.Errorf("case %d CanUse(devs) = %v", i, got)
		}
		if got := a.CanUse(c.id, open); got != c.open {
			t.Errorf("case %d CanUse(*) = %v", i, got)
		}
		if got := a.CanUse(c.id, closed); got != c.none {
			t.Errorf("case %d CanUse(none) = %v", i, got)
		}
	}
}
