package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
)

func user(name string, groups ...string) *argocd.Identity {
	return &argocd.Identity{Username: name, Groups: groups}
}

func TestIsAdmin(t *testing.T) {
	a := New([]string{"admin"}, []string{"zea-admins"}, nil)
	cases := []struct {
		id   *argocd.Identity
		want bool
	}{
		{nil, false},
		{user("admin"), true},
		{user("bob", "x", "zea-admins"), true},
		{&argocd.Identity{UserID: "admin"}, true},
		{user("alice", "devs"), false},
	}
	for i, c := range cases {
		if got := a.IsAdmin(c.id); got != c.want {
			t.Errorf("case %d IsAdmin = %v", i, got)
		}
	}
	acc, err := a.For(context.Background(), user("admin"))
	if err != nil || !acc.IsAdmin() || !acc.Can(ResourceStreams, ActionEdit, "anything") {
		t.Fatalf("admin access = %+v, %v", acc, err)
	}
}

const samplePolicy = `
roles:
  - name: dev
    rules:
      - resource: streams
        pattern: "dev-*"
        actions: [run]
      - resource: connections
        pattern: "*"
        actions: [view]
  - name: release
    rules:
      - resource: streams
        pattern: "prod-*"
        actions: [run, view]
      - resource: registries
        actions: [edit]
bindings:
  - group: devs
    roles: [dev]
  - user: carol
    roles: [release]
  - group: "*"
    roles: []
`

func TestEvaluate(t *testing.T) {
	p, err := ParsePolicy(samplePolicy)
	if err != nil {
		t.Fatal(err)
	}
	if problems := p.Problems(); len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if p.Roles[1].Rules[0].Actions[0] != ActionView || p.Roles[1].Rules[1].Pattern != "*" {
		t.Fatalf("not normalized: %+v", p.Roles[1])
	}

	dev := Evaluate(p, user("alice", "devs"))
	checks := []struct {
		acc                    *Access
		resource, action, name string
		want                   bool
	}{
		{dev, ResourceStreams, ActionRun, "dev-api", true},
		{dev, ResourceStreams, ActionView, "dev-api", true}, // run implies view
		{dev, ResourceStreams, ActionEdit, "dev-api", false},
		{dev, ResourceStreams, ActionRun, "prod-api", false},
		{dev, ResourceConnections, ActionView, "x", true},
		{dev, ResourceConnections, ActionRun, "x", false},
		{dev, ResourceRegistries, ActionView, "r", false},
	}
	carol := Evaluate(p, user("carol"))
	checks = append(checks, []struct {
		acc                    *Access
		resource, action, name string
		want                   bool
	}{
		{carol, ResourceStreams, ActionRun, "prod-api", true},
		{carol, ResourceStreams, ActionView, "dev-api", false},
		{carol, ResourceRegistries, ActionView, "any", true},
		{nil, ResourceStreams, ActionView, "dev-api", false},
	}...)
	for i, c := range checks {
		if got := c.acc.Can(c.resource, c.action, c.name); got != c.want {
			t.Errorf("check %d: Can(%s, %s, %s) = %v", i, c.resource, c.action, c.name, got)
		}
	}
	if got := strings.Join(dev.Actions(ResourceStreams, "dev-x"), ","); got != "view,run" {
		t.Errorf("actions = %q", got)
	}
	if s := dev.Summary(); strings.Join(s[ResourceStreams], ",") != "view,run" || len(s[ResourceRegistries]) != 0 {
		t.Errorf("summary = %v", s)
	}
	if !dev.CanAny(ResourceStreams, ActionRun) || dev.CanAny(ResourceStreams, ActionEdit) {
		t.Errorf("CanAny")
	}
	if strings.Join(carol.Roles(), ",") != "release" {
		t.Errorf("roles = %v", carol.Roles())
	}
}

func TestPolicyProblems(t *testing.T) {
	p := Policy{
		Roles: []Role{
			{Name: "Bad Name", Rules: []Rule{{Resource: "streams", Actions: []string{"run"}}}},
			{Name: "ok", Rules: []Rule{
				{Resource: "apps", Actions: []string{"view"}},
				{Resource: "registries", Actions: []string{"run"}},
				{Resource: "streams", Pattern: "[", Actions: []string{"view"}},
				{Resource: "streams"},
			}},
			{Name: "ok"},
		},
		Bindings: []Binding{
			{Group: "devs", User: "x"},
			{},
			{Group: "devs", Roles: []string{"missing"}},
			{Group: "devs"},
		},
	}
	p.Normalize()
	got := []string{}
	for _, pr := range p.Problems() {
		got = append(got, pr.String())
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		`roles[0].name: role name "Bad Name"`,
		`roles[1].rules[0].resource: unknown resource "apps"`,
		`roles[1].rules[1].actions: registries do not support action "run"`,
		`roles[1].rules[2].pattern: invalid pattern "["`,
		`roles[1].rules[3].actions: select at least one action`,
		`roles[2].name: role "ok" is defined more than once`,
		`bindings[0]: set either group or user, not both`,
		`bindings[1]: set a group or a user`,
		`bindings[2].roles: role "missing" does not exist`,
		`bindings[3]: group "devs" is bound more than once`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing problem %q in:\n%s", want, all)
		}
	}
	if _, err := ParsePolicy("roles: []\nextra: 1\n"); err == nil {
		t.Errorf("unknown field accepted")
	}
}

func TestStoreAndCache(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	a := New(nil, nil, store)
	doc, err := a.Policy(ctx)
	if err != nil || doc.Exists || !doc.Editable {
		t.Fatalf("empty doc = %+v, %v", doc, err)
	}
	p, _ := ParsePolicy(samplePolicy)
	if _, err := a.SavePolicy(ctx, p, "7"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale create: %v", err)
	}
	doc, err = a.SavePolicy(ctx, p, "")
	if err != nil || !doc.Exists {
		t.Fatalf("save: %+v, %v", doc, err)
	}
	acc, err := a.For(ctx, user("alice", "devs"))
	if err != nil || !acc.Can(ResourceStreams, ActionRun, "dev-1") {
		t.Fatalf("access after save: %v", err)
	}
	bad := Policy{Roles: []Role{{Name: "x", Rules: []Rule{{Resource: "nope", Actions: []string{"view"}}}}}}
	var ve *ValidationError
	if _, err := a.SavePolicy(ctx, bad, doc.Version); !errors.As(err, &ve) || len(ve.Problems) != 1 {
		t.Fatalf("invalid save: %v", err)
	}

	// An invalid policy stored outside Zea grants nothing.
	store.SetRaw("roles: [{name: x, rules: [{resource: nope, actions: [view]}]}]", true)
	a = New(nil, nil, store)
	acc, err = a.For(ctx, user("alice", "devs"))
	if err == nil || acc.CanAny(ResourceStreams, ActionView) {
		t.Fatalf("invalid stored policy: %v", err)
	}
	if _, err := a.SavePolicy(ctx, p, "2"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only save: %v", err)
	}
}

func TestLegacyPolicy(t *testing.T) {
	conns := []LegacyConnection{
		{Name: "shared", Groups: []string{"devs"}},
		{Name: "secret", Groups: []string{"ops"}},
		{Name: "gitops", Groups: []string{"*"}},
		{Name: "both", Groups: []string{"devs", "org:Team A"}},
	}
	strms := []LegacyStream{
		{Name: "pub", Connections: []string{"shared", "gitops"}},
		{Name: "ops-only", Connections: []string{"shared", "secret"}},
		{Name: "git", Connections: []string{"gitops"}},
		{Name: "empty"},
	}
	p := LegacyPolicy(conns, strms)
	p.Normalize()
	if problems := p.Problems(); len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	roles := []string{}
	for _, r := range p.Roles {
		roles = append(roles, r.Name)
	}
	if got := strings.Join(roles, ","); got != "everyone,legacy-devs,legacy-ops,legacy-org-Team-A" {
		t.Fatalf("roles = %s", got)
	}
	can := func(groups []string, res, name string) bool {
		return Evaluate(p, user("u", groups...)).Can(res, ActionRun, name)
	}
	cases := []struct {
		groups []string
		res    string
		name   string
		want   bool
	}{
		{[]string{"devs"}, ResourceConnections, "shared", true},
		{[]string{"devs"}, ResourceConnections, "secret", false},
		{[]string{"devs"}, ResourceConnections, "gitops", true},
		{[]string{"devs"}, ResourceStreams, "pub", true},
		{[]string{"devs"}, ResourceStreams, "git", true},
		{[]string{"devs"}, ResourceStreams, "ops-only", false},
		{[]string{"devs"}, ResourceStreams, "empty", false},
		{[]string{"other"}, ResourceStreams, "git", true},
		{[]string{"other"}, ResourceStreams, "pub", false},
		{[]string{"org:Team A"}, ResourceConnections, "both", true},
	}
	for i, c := range cases {
		if got := can(c.groups, c.res, c.name); got != c.want {
			t.Errorf("case %d %v %s/%s = %v", i, c.groups, c.res, c.name, got)
		}
	}

	ctx := context.Background()
	store := NewMemoryPolicyStore()
	if created, err := Migrate(ctx, store, conns, strms); !created || err != nil {
		t.Fatalf("migrate: %v %v", created, err)
	}
	if created, err := Migrate(ctx, store, nil, nil); created || err != nil {
		t.Fatalf("second migrate: %v %v", created, err)
	}
	doc, _ := store.Load(ctx)
	if len(doc.Policy.Roles) != 4 || doc.Error != "" {
		t.Fatalf("migrated doc = %+v", doc)
	}
}
