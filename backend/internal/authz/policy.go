package authz

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Resource types a rule can grant access to.
const (
	ResourceConnections = "connections"
	ResourceStreams     = "streams"
	ResourceRegistries  = "registries"
)

// Actions a rule can grant. Every action also grants view.
const (
	ActionView = "view"
	ActionRun  = "run"
	ActionEdit = "edit"
)

// Everyone as a binding group matches every user who can open Zea.
const Everyone = "*"

// Policy limits keep the ConfigMap and every evaluation small.
const (
	MaxRoles        = 200
	MaxRulesPerRole = 100
	MaxBindings     = 500
	maxPatternLen   = 253
	maxSubjectLen   = 256
	maxDescription  = 500
)

// ResourceInfo describes a resource type and the actions it supports.
type ResourceInfo struct {
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

var resources = []ResourceInfo{
	{Name: ResourceStreams, Actions: []string{ActionView, ActionRun, ActionEdit}},
	{Name: ResourceConnections, Actions: []string{ActionView, ActionRun, ActionEdit}},
	{Name: ResourceRegistries, Actions: []string{ActionView, ActionEdit}},
}

// Resources lists the resource types in display order.
func Resources() []ResourceInfo {
	out := make([]ResourceInfo, len(resources))
	for i, r := range resources {
		out[i] = ResourceInfo{Name: r.Name, Actions: slices.Clone(r.Actions)}
	}
	return out
}

func actionsOf(resource string) []string {
	for _, r := range resources {
		if r.Name == resource {
			return r.Actions
		}
	}
	return nil
}

// Rule grants actions on the resources whose name matches Pattern.
type Rule struct {
	Resource string `json:"resource" yaml:"resource"`
	// Pattern is a glob over resource names ("*", "deploy-*", "app-?").
	Pattern string   `json:"pattern" yaml:"pattern"`
	Actions []string `json:"actions" yaml:"actions,flow"`
}

// Role is a named set of rules.
type Role struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Rules       []Rule `json:"rules" yaml:"rules"`
}

// Binding gives roles to an SSO group or to an Argo CD user. Exactly one of
// Group and User is set.
type Binding struct {
	Group string   `json:"group,omitempty" yaml:"group,omitempty"`
	User  string   `json:"user,omitempty" yaml:"user,omitempty"`
	Roles []string `json:"roles" yaml:"roles,flow"`
}

// Policy is the access policy edited in the UI. Admins from the chart values
// are not part of it and always have full access.
type Policy struct {
	Roles    []Role    `json:"roles" yaml:"roles"`
	Bindings []Binding `json:"bindings" yaml:"bindings"`
}

// Problem is a policy validation error; Path points at the offending field.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

var roleNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Normalize trims values, defaults empty patterns to "*" and orders actions
// canonically. It never drops entries, so Problems still sees them.
func (p *Policy) Normalize() {
	if p.Roles == nil {
		p.Roles = []Role{}
	}
	if p.Bindings == nil {
		p.Bindings = []Binding{}
	}
	for i := range p.Roles {
		r := &p.Roles[i]
		r.Name = strings.TrimSpace(r.Name)
		r.Description = strings.TrimSpace(r.Description)
		if r.Rules == nil {
			r.Rules = []Rule{}
		}
		for j := range r.Rules {
			rule := &r.Rules[j]
			rule.Resource = strings.TrimSpace(rule.Resource)
			rule.Pattern = strings.TrimSpace(rule.Pattern)
			if rule.Pattern == "" {
				rule.Pattern = "*"
			}
			rule.Actions = canonicalActions(rule.Resource, rule.Actions)
		}
	}
	for i := range p.Bindings {
		b := &p.Bindings[i]
		b.Group = strings.TrimSpace(b.Group)
		b.User = strings.TrimSpace(b.User)
		roles := []string{}
		for _, r := range b.Roles {
			if r = strings.TrimSpace(r); r != "" && !slices.Contains(roles, r) {
				roles = append(roles, r)
			}
		}
		b.Roles = roles
	}
}

// canonicalActions orders known actions like Resources() and keeps unknown
// ones at the end, so they are reported instead of silently dropped.
func canonicalActions(resource string, in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range actionsOf(resource) {
		for _, x := range in {
			if strings.TrimSpace(x) == a && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	for _, x := range in {
		if x = strings.TrimSpace(x); !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Problems validates a normalized policy.
func (p *Policy) Problems() []Problem {
	var out []Problem
	add := func(path, format string, args ...any) {
		out = append(out, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	if len(p.Roles) > MaxRoles {
		add("roles", "at most %d roles are allowed", MaxRoles)
	}
	if len(p.Bindings) > MaxBindings {
		add("bindings", "at most %d bindings are allowed", MaxBindings)
	}
	roles := map[string]bool{}
	for i, r := range p.Roles {
		at := fmt.Sprintf("roles[%d]", i)
		switch {
		case !roleNameRe.MatchString(r.Name):
			add(at+".name", "role name %q must start with a letter or digit and contain only letters, digits, '.', '_' and '-' (max 63)", r.Name)
		case roles[r.Name]:
			add(at+".name", "role %q is defined more than once", r.Name)
		}
		roles[r.Name] = true
		if len(r.Description) > maxDescription {
			add(at+".description", "description is longer than %d characters", maxDescription)
		}
		if len(r.Rules) > MaxRulesPerRole {
			add(at+".rules", "at most %d rules per role are allowed", MaxRulesPerRole)
		}
		for j, rule := range r.Rules {
			rat := fmt.Sprintf("%s.rules[%d]", at, j)
			allowed := actionsOf(rule.Resource)
			if allowed == nil {
				add(rat+".resource", "unknown resource %q (expected %s)", rule.Resource, resourceNames())
				continue
			}
			if len(rule.Pattern) > maxPatternLen {
				add(rat+".pattern", "pattern is longer than %d characters", maxPatternLen)
			} else if _, err := path.Match(rule.Pattern, ""); err != nil {
				add(rat+".pattern", "invalid pattern %q", rule.Pattern)
			}
			if len(rule.Actions) == 0 {
				add(rat+".actions", "select at least one action")
			}
			for _, a := range rule.Actions {
				if !slices.Contains(allowed, a) {
					add(rat+".actions", "%s do not support action %q (expected %s)", rule.Resource, a, strings.Join(allowed, ", "))
				}
			}
		}
	}
	subjects := map[string]bool{}
	for i, b := range p.Bindings {
		at := fmt.Sprintf("bindings[%d]", i)
		key := ""
		switch {
		case b.Group != "" && b.User != "":
			add(at, "set either group or user, not both")
		case b.Group != "":
			key = "group:" + b.Group
		case b.User != "":
			key = "user:" + b.User
		default:
			add(at, "set a group or a user")
		}
		if len(b.Group) > maxSubjectLen || len(b.User) > maxSubjectLen {
			add(at, "group or user is longer than %d characters", maxSubjectLen)
		}
		if key != "" {
			if subjects[key] {
				add(at, "%s is bound more than once; merge its roles into one binding", subjectLabel(b))
			}
			subjects[key] = true
		}
		for _, r := range b.Roles {
			if !roles[r] {
				add(at+".roles", "role %q does not exist", r)
			}
		}
	}
	return out
}

func subjectLabel(b Binding) string {
	if b.User != "" {
		return fmt.Sprintf("user %q", b.User)
	}
	if b.Group == Everyone {
		return "everyone (*)"
	}
	return fmt.Sprintf("group %q", b.Group)
}

func resourceNames() string {
	names := make([]string, len(resources))
	for i, r := range resources {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// ParsePolicy reads policy YAML. Unknown fields are errors.
func ParsePolicy(raw string) (Policy, error) {
	var p Policy
	if strings.TrimSpace(raw) != "" {
		dec := yaml.NewDecoder(strings.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&p); err != nil {
			return Policy{}, fmt.Errorf("invalid %s: %v", KeyPolicy, err)
		}
	}
	p.Normalize()
	return p, nil
}

// FormatPolicy renders a policy as YAML.
func FormatPolicy(p Policy) (string, error) {
	p.Normalize()
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
