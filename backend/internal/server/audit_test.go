package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/audit"
)

func TestAuditRecordsChanges(t *testing.T) {
	e := newEnv(false)
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/shared", nil, as("admin", "")); code != http.StatusNoContent {
		t.Fatalf("delete status = %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/secret", nil); code < 400 {
		t.Fatalf("forbidden delete status = %d", code)
	}
	e.do(t, "POST", "/api/v1/images/match", map[string]any{"images": []string{"x"}})
	e.do(t, "GET", "/api/v1/connections", nil)

	if code, _ := e.do(t, "GET", "/api/v1/audit", nil); code != http.StatusForbidden {
		t.Fatalf("non-admin audit status = %d", code)
	}
	e.audit.Flush(context.Background())
	code, out := e.do(t, "GET", "/api/v1/audit", nil, as("admin", ""))
	if code != http.StatusOK {
		t.Fatalf("audit status = %d %v", code, out)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if out["day"] != today || out["retention"] != float64(30) {
		t.Fatalf("audit day = %v retention = %v", out["day"], out["retention"])
	}
	events := out["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	failed, ok := events[0].(map[string]any), events[1].(map[string]any)
	if ok["user"] != "admin" || ok["action"] != "connection.delete" || ok["resource"] != "connection" || ok["name"] != "shared" || ok["status"] != float64(204) {
		t.Fatalf("delete event = %v", ok)
	}
	if failed["user"] != "alice" || failed["name"] != "secret" || failed["status"].(float64) < 400 || failed["error"] == "" {
		t.Fatalf("failed event = %v", failed)
	}
	if code, _ := e.do(t, "GET", "/api/v1/audit?day=yesterday", nil, as("admin", "")); code != http.StatusBadRequest {
		t.Fatalf("bad day status = %d", code)
	}
}

func TestAuditViewShowsVisibleItemsOnly(t *testing.T) {
	e := newEnv(false)
	e.policy.SetRaw(`roles:
  - name: auditor
    rules:
      - {resource: audit, pattern: "*", actions: [view]}
      - {resource: connections, pattern: shared, actions: [view]}
      - {resource: streams, pattern: none, actions: [view]}
bindings:
  - {group: auditors, roles: [auditor]}
`, true)
	for _, ev := range []audit.Event{
		{Action: "connection.update", Resource: "connection", Name: "shared", Status: 200},
		{Action: "connection.update", Resource: "connection", Name: "secret", Status: 200},
		{Action: "policy.save", Resource: "policy", Status: 200},
		{Action: "stream.order", Resource: "stream", Name: "order", Status: 200},
		{Action: "stream.update", Resource: "stream", Name: "other", Status: 200},
	} {
		e.audit.Record(ev)
	}
	e.audit.Flush(context.Background())

	if code, _ := e.do(t, "GET", "/api/v1/audit", nil, as("bob", "devs")); code != http.StatusForbidden {
		t.Fatalf("audit without audit view status = %d", code)
	}
	code, out := e.do(t, "GET", "/api/v1/audit", nil, as("carol", "auditors"))
	if code != http.StatusOK {
		t.Fatalf("auditor status = %d %v", code, out)
	}
	var got []string
	for _, ev := range out["events"].([]any) {
		m := ev.(map[string]any)
		got = append(got, fmt.Sprint(m["action"], " ", m["name"]))
	}
	if strings.Join(got, ",") != "stream.order order,connection.update shared" {
		t.Fatalf("auditor events = %v", got)
	}
	if _, out := e.do(t, "GET", "/api/v1/audit", nil, as("admin", "")); len(out["events"].([]any)) != 5 {
		t.Fatalf("admin events = %v", out["events"])
	}
	if _, me := e.do(t, "GET", "/api/v1/me", nil, as("carol", "auditors")); fmt.Sprint(me["permissions"].(map[string]any)["audit"]) != "[view]" {
		t.Fatalf("me permissions = %v", me["permissions"])
	}
}

func TestAuditRulesFilterByActionAndAuthor(t *testing.T) {
	e := newEnv(false)
	e.policy.SetRaw(`roles:
  - name: qa-audit
    rules:
      - {resource: audit, pattern: "*", actions: [view], authors: ["group:zea-*", "user:root"]}
      - {resource: connections, pattern: "*", actions: [view]}
  - name: user-audit
    rules:
      - {resource: audit, pattern: "connection.run.*", actions: [view], authors: ["group:qa"]}
      - {resource: connections, pattern: "*", actions: [view]}
bindings:
  - {group: qa, roles: [qa-audit]}
  - {group: users, roles: [user-audit]}
`, true)
	for _, ev := range []audit.Event{
		{User: "boss", Groups: []string{"zea-admins"}, Action: "connection.update", Resource: "connection", Name: "shared", Status: 200},
		{User: "root", Action: "connection.delete", Resource: "connection", Name: "secret", Status: 204},
		{User: "quinn", Groups: []string{"qa"}, Action: "connection.run.start", Resource: "connection", Name: "shared", Status: 201},
		{User: "quinn", Groups: []string{"qa"}, Action: "connection.update", Resource: "connection", Name: "shared", Status: 200},
		{User: "zed", Action: "connection.run.start", Resource: "connection", Name: "shared", Status: 201},
	} {
		e.audit.Record(ev)
	}
	e.audit.Flush(context.Background())
	seen := func(user, groups string) string {
		code, out := e.do(t, "GET", "/api/v1/audit", nil, as(user, groups))
		if code != http.StatusOK {
			t.Fatalf("%s audit status = %d %v", user, code, out)
		}
		var got []string
		for _, ev := range out["events"].([]any) {
			m := ev.(map[string]any)
			got = append(got, fmt.Sprint(m["user"], " ", m["action"]))
		}
		return strings.Join(got, ",")
	}
	if got := seen("quinn", "qa"); got != "root connection.delete,boss connection.update" {
		t.Fatalf("qa sees %q", got)
	}
	if got := seen("ursula", "users"); got != "quinn connection.run.start" {
		t.Fatalf("users see %q", got)
	}
}

func TestAuditStoresPolicyGroups(t *testing.T) {
	e := newEnv(false)
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/shared", nil, as("boss", "random,zea-admins,devs")); code != http.StatusNoContent {
		t.Fatalf("delete status = %d", code)
	}
	d, err := e.audit.Get(context.Background(), time.Now().UTC().Format("2006-01-02"))
	if err != nil || len(d.Events) != 1 {
		t.Fatalf("events = %v %v", d, err)
	}
	if got := strings.Join(d.Events[0].Groups, ","); got != "zea-admins,devs" {
		t.Fatalf("event groups = %q", got)
	}
}

func TestAuditNamesCreatedStream(t *testing.T) {
	e := newEnv(false)
	body := map[string]any{"name": "fresh", "stages": []any{map[string]any{"steps": []any{
		map[string]any{"id": "a", "connection": "shared", "pipeline": "1", "ref": "main"}}}}}
	code, out := e.do(t, "POST", "/api/v1/streams", body, as("admin", ""))
	if code != http.StatusCreated {
		t.Fatalf("create status = %d %v", code, out)
	}
	d, err := e.audit.Get(context.Background(), time.Now().UTC().Format("2006-01-02"))
	if err != nil || len(d.Events) != 1 {
		t.Fatalf("events = %v %v", d, err)
	}
	if ev := d.Events[0]; ev.Action != "stream.create" || ev.Name != "fresh" || ev.Status != http.StatusCreated {
		t.Fatalf("event = %+v", ev)
	}
}
