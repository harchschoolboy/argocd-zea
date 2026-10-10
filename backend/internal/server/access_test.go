package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// setPolicy replaces the policy through the API, as an admin would.
func (e *testEnv) setPolicy(t *testing.T, policy map[string]any) {
	t.Helper()
	admin := as("admin", "")
	_, cur := e.do(t, "GET", "/api/v1/policy", nil, admin)
	code, body := e.do(t, "PUT", "/api/v1/policy", map[string]any{"policy": policy, "version": cur["version"]}, admin)
	if code != http.StatusOK {
		t.Fatalf("save policy: %d %v", code, body)
	}
}

func rule(resource, pattern string, actions ...string) map[string]any {
	return map[string]any{"resource": resource, "pattern": pattern, "actions": actions}
}

func role(name string, rules ...map[string]any) map[string]any {
	return map[string]any{"name": name, "rules": rules}
}

func TestPolicyAPI(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	if code, _ := e.do(t, "GET", "/api/v1/policy", nil); code != http.StatusForbidden {
		t.Fatalf("non-admin get: %d", code)
	}
	code, body := e.do(t, "GET", "/api/v1/policy", nil, admin)
	if code != http.StatusOK || body["exists"] != true || body["editable"] != true || body["version"] == "" ||
		!strings.Contains(body["yaml"].(string), "legacy-devs") || mustJSON(body["admins"]) != `{"groups":["zea-admins"],"users":["admin"]}` {
		t.Fatalf("get: %d %v", code, body)
	}
	version := body["version"]

	bad := map[string]any{"roles": []any{role("x", rule("nope", "*", "view"))}}
	if code, body := e.do(t, "PUT", "/api/v1/policy", map[string]any{"policy": bad, "version": version}, admin); code != http.StatusBadRequest ||
		!strings.Contains(body["error"].(string), `unknown resource "nope"`) {
		t.Fatalf("invalid save: %d %v", code, body)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/policy", map[string]any{"policy": map[string]any{}, "version": "stale"}, admin); code != http.StatusConflict {
		t.Fatalf("stale save: %d", code)
	}

	code, body = e.do(t, "POST", "/api/v1/policy/validate", map[string]any{"yaml": "roles:\n  - name: r\n    rules:\n      - resource: streams\n        actions: [run]\nbindings:\n  - group: devs\n    roles: [r, missing]\n"}, admin)
	if code != http.StatusOK || len(body["problems"].([]any)) != 1 || !strings.Contains(mustJSON(body["policy"]), `"pattern":"*"`) {
		t.Fatalf("validate: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/policy/validate", map[string]any{"yaml": "roles: [\n"}, admin); code != http.StatusBadRequest {
		t.Fatalf("validate broken yaml: %d", code)
	}

	draft := map[string]any{
		"roles":    []any{role("runner", rule("streams", "p*", "run"))},
		"bindings": []any{map[string]any{"group": "qa", "roles": []string{"runner"}}},
	}
	code, body = e.do(t, "POST", "/api/v1/policy/evaluate", map[string]any{"policy": draft, "user": "zoe", "groups": []string{"qa"}}, admin)
	if code != http.StatusOK || mustJSON(body["roles"]) != `["runner"]` ||
		mustJSON(body["items"].(map[string]any)["streams"]) != `[{"actions":["view","run"],"name":"pub"}]` ||
		mustJSON(body["items"].(map[string]any)["connections"]) != `[]` {
		t.Fatalf("evaluate: %d %v", code, body)
	}

	e.setPolicy(t, draft)
	_, body = e.do(t, "GET", "/api/v1/me", nil, as("zoe", "qa"))
	if mustJSON(body["permissions"]) != `{"connections":[],"registries":[],"streams":["view","run"]}` {
		t.Fatalf("me permissions: %v", body)
	}
	// The migrated roles are gone: alice no longer sees anything.
	if _, body := e.do(t, "GET", "/api/v1/connections", nil); len(body["connections"].([]any)) != 0 {
		t.Fatalf("alice still sees connections: %v", body)
	}
}

func TestInvalidStoredPolicyGrantsNothing(t *testing.T) {
	e := newEnv(false)
	e.policy.SetRaw("roles: [{name: x, rules: [{resource: nope, actions: [view]}]}]\n", true)
	if _, body := e.do(t, "GET", "/api/v1/streams", nil); len(body["streams"].([]any)) != 0 {
		t.Fatalf("invalid policy grants: %v", body)
	}
	if _, body := e.do(t, "GET", "/api/v1/streams", nil, as("admin", "")); len(body["streams"].([]any)) != 4 {
		t.Fatalf("admin access: %v", body)
	}
	_, body := e.do(t, "GET", "/api/v1/policy", nil, as("admin", ""))
	if body["editable"] != false || !strings.Contains(body["error"].(string), `unknown resource "nope"`) {
		t.Fatalf("policy doc: %v", body)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/policy", map[string]any{"policy": map[string]any{}, "version": body["version"]}, as("admin", "")); code != http.StatusConflict {
		t.Fatalf("read-only save: %d", code)
	}
}

func TestDelegatedStreamRun(t *testing.T) {
	e := newEnv(false)
	e.setPolicy(t, map[string]any{
		"roles":    []any{role("runner", rule("streams", "pub", "run"))},
		"bindings": []any{map[string]any{"user": "zoe", "roles": []string{"runner"}}},
	})
	zoe := as("zoe", "")
	if code, _ := e.do(t, "GET", "/api/v1/connections/shared/branches", nil, zoe); code != http.StatusNotFound {
		t.Fatalf("connection branches: %d", code)
	}
	code, body := e.do(t, "GET", "/api/v1/streams/pub/branches?connection=shared", nil, zoe)
	if code != http.StatusOK || body["defaultBranch"] != "main" {
		t.Fatalf("stream branches: %d %v", code, body)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/branches?connection=secret", nil, zoe); code != http.StatusNotFound {
		t.Fatalf("branches of unused connection: %d", code)
	}
	code, body = e.do(t, "GET", "/api/v1/streams/pub", nil, zoe)
	if code != http.StatusOK || mustJSON(body["actions"]) != `["view","run"]` {
		t.Fatalf("get: %d %v", code, body)
	}
	code, body = e.do(t, "POST", "/api/v1/streams/pub/runs", map[string]any{"params": map[string]string{"branch": "dev"}}, zoe)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	id := body["id"].(string)
	e.eng.ReconcileAll(context.Background())

	code, body = e.do(t, "GET", "/api/v1/streams/pub/runs/"+id+"/jobs/42?step=build", nil, zoe)
	if code != http.StatusOK || !strings.Contains(mustJSON(body), `"jobs"`) {
		t.Fatalf("jobs: %d %v", code, body)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs/"+id+"/jobs/41?step=build", nil, zoe); code != http.StatusNotFound {
		t.Fatalf("jobs of a foreign run: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs/"+id+"/jobs/42?step=deploy", nil, zoe); code != http.StatusNotFound {
		t.Fatalf("jobs of a step that did not start it: %d", code)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/streams/pub", map[string]any{"stages": []any{}}, zoe); code != http.StatusForbidden {
		t.Fatalf("edit: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/git", nil, zoe); code != http.StatusNotFound {
		t.Fatalf("other stream: %d", code)
	}
}

func TestStreamSaveNeedsConnectionRun(t *testing.T) {
	e := newEnv(false)
	e.setPolicy(t, map[string]any{
		"roles": []any{role("author",
			rule("streams", "team-*", "edit", "run"),
			rule("connections", "gitops", "run"),
			rule("connections", "shared", "view"))},
		"bindings": []any{map[string]any{"group": "team", "roles": []string{"author"}}},
	})
	u := as("tom", "team")
	stages := func(conn string) []any {
		return []any{map[string]any{"steps": []any{map[string]any{"id": "a", "connection": conn, "pipeline": "1", "ref": "main"}}}}
	}
	if code, body := e.do(t, "POST", "/api/v1/streams", map[string]any{"name": "team-a", "stages": stages("shared")}, u); code != http.StatusForbidden ||
		!strings.Contains(body["error"].(string), "shared") {
		t.Fatalf("create with view-only connection: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams", map[string]any{"name": "other", "stages": stages("gitops")}, u); code != http.StatusForbidden {
		t.Fatalf("create outside pattern: %d", code)
	}
	code, body := e.do(t, "POST", "/api/v1/streams", map[string]any{"name": "team-a", "stages": stages("gitops")}, u)
	if code != http.StatusCreated || mustJSON(body["actions"]) != `["view","run","edit"]` {
		t.Fatalf("create: %d %v", code, body)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/streams/team-a", map[string]any{"stages": stages("shared"), "version": body["version"]}, u); code != http.StatusForbidden {
		t.Fatalf("update to view-only connection: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/streams/team-a", nil, u); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
}

func TestConnectionEditByPattern(t *testing.T) {
	e := newEnv(false)
	e.setPolicy(t, map[string]any{
		"roles": []any{role("maint",
			rule("connections", "team-*", "edit"),
			rule("connections", "shared", "run"))},
		"bindings": []any{map[string]any{"user": "tom", "roles": []string{"maint"}}},
	})
	u := as("tom", "")
	in := map[string]any{"name": "team-x", "provider": "fake", "url": "https://x/o/x", "credentials": map[string]string{"token": "t"}}
	if code, body := e.do(t, "POST", "/api/v1/connections", in, u); code != http.StatusCreated || body["credentialKeys"] == nil {
		t.Fatalf("create: %d %v", code, body)
	}
	in["name"] = "other"
	if code, _ := e.do(t, "POST", "/api/v1/connections", in, u); code != http.StatusForbidden {
		t.Fatalf("create outside pattern: %d", code)
	}
	in["name"] = "team-y"
	in["images"] = []any{map[string]any{"registry": "do", "repository": "^web$"}}
	if code, body := e.do(t, "POST", "/api/v1/connections", in, u); code != http.StatusForbidden || !strings.Contains(body["error"].(string), "do") {
		t.Fatalf("create with registry without access: %d %v", code, body)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/shared", nil, u); code != http.StatusForbidden {
		t.Fatalf("delete run-only: %d", code)
	}
	_, body := e.do(t, "GET", "/api/v1/connections", nil, u)
	if got := strings.Join(names(body), ","); got != "shared,team-x" || strings.Contains(mustJSON(body["connections"].([]any)[0]), "credentialKeys") {
		t.Fatalf("list: %v", body)
	}

	// Stored credentials are not reused for a draft of a Connection the
	// user may not edit, so they cannot be sent to another URL.
	draft := map[string]any{"name": "shared", "provider": "fake", "url": "https://evil/o/r", "credentials": map[string]string{}}
	if code, body := e.do(t, "POST", "/api/v1/test-connection", draft, u); code != http.StatusBadRequest {
		t.Fatalf("draft of foreign connection: %d %v", code, body)
	}
}
