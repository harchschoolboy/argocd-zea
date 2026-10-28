package authz

import (
	"strings"
	"testing"
)

func TestAuditAuthorsValidation(t *testing.T) {
	p, err := ParsePolicy(`roles:
  - name: a
    rules:
      - {resource: audit, pattern: "stream.*", actions: [view], authors: [" group:qa ", "user:x", "user:x"]}
      - {resource: audit, pattern: "*", actions: [view], authors: ["user:y", "*"]}
      - {resource: streams, pattern: "*", actions: [view], authors: ["group:qa"]}
      - {resource: audit, pattern: "*", actions: [view], authors: ["qa", "group:", "user:[x"]}
bindings: []
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Roles[0].Rules[0].Authors, ","); got != "group:qa,user:x" {
		t.Fatalf("authors = %q", got)
	}
	if p.Roles[0].Rules[1].Authors != nil {
		t.Fatalf("* authors = %v", p.Roles[0].Rules[1].Authors)
	}
	var msgs []string
	for _, pr := range p.Problems() {
		msgs = append(msgs, pr.String())
	}
	want := []string{
		"roles[0].rules[2].authors: only audit rules have authors",
		`roles[0].rules[3].authors: author "qa" must be user:<name> or group:<name>`,
		`roles[0].rules[3].authors: author "group:" must be user:<name> or group:<name>`,
		`roles[0].rules[3].authors: invalid pattern in author "user:[x"`,
	}
	if strings.Join(msgs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("problems:\n%s", strings.Join(msgs, "\n"))
	}
}
