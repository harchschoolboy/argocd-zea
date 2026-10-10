package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

// fakeRegistryKind serves a fixed set of repositories for token "regtok".
type fakeRegistryKind struct{}

func (fakeRegistryKind) Info() registries.KindInfo {
	return registries.KindInfo{ID: "fakereg", Name: "Fake registry", CredentialModes: []providers.CredentialMode{
		{ID: "token", Fields: []providers.CredentialField{{Key: "token", Secret: true}}},
	}}
}

func (fakeRegistryKind) Validate(*registries.Registry) error { return nil }

func (fakeRegistryKind) ListRepositories(_ context.Context, r *registries.Registry) ([]registries.Repository, error) {
	if r.Credentials["token"] != "regtok" {
		return nil, &providers.UpstreamError{Provider: "Fake registry", Status: 401, Message: "bad token"}
	}
	return []registries.Repository{{Name: "web-main"}, {Name: "web-dev"}, {Name: "other"}}, nil
}

func (fakeRegistryKind) ListTags(_ context.Context, _ *registries.Registry, repo string) ([]registries.Tag, error) {
	return []registries.Tag{
		{Name: "1111111", PushedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Name: "latest", PushedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}, nil
}

func (fakeProvider) CommitURL(c *connections.Connection, sha string) string {
	return c.URL + "/commit/" + sha
}

func TestRegistriesNeedAccess(t *testing.T) {
	e := newEnv(false)
	code, body := e.do(t, "GET", "/api/v1/registries", nil)
	if code != http.StatusOK || mustJSON(body["registries"]) != `[]` {
		t.Fatalf("list without access: %d %v", code, body)
	}
	for _, req := range [][3]any{
		{"GET", "/api/v1/registry-kinds", http.StatusForbidden}, {"POST", "/api/v1/registries", http.StatusForbidden},
		{"DELETE", "/api/v1/registries/do", http.StatusNotFound}, {"POST", "/api/v1/registries/do/test", http.StatusNotFound},
		{"POST", "/api/v1/images/preview", http.StatusForbidden},
	} {
		if code, _ := e.do(t, req[0].(string), req[1].(string), map[string]any{}); code != req[2].(int) {
			t.Fatalf("%s %s without access: %d", req[0], req[1], code)
		}
	}
}

func TestRegistryLifecycle(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")

	code, body := e.do(t, "GET", "/api/v1/registries", nil, admin)
	if code != http.StatusOK || strings.Contains(mustJSON(body), "regtok") {
		t.Fatalf("list: %d %s", code, mustJSON(body))
	}
	regs := body["registries"].([]any)
	if len(regs) != 2 || regs[1].(map[string]any)["name"] != "do" || mustJSON(regs[1].(map[string]any)["usedBy"]) != `["shared"]` {
		t.Fatalf("list body: %s", mustJSON(body))
	}

	in := map[string]any{"name": "new", "kind": "fakereg", "url": "reg.example.com/new", "credentials": map[string]string{"token": "regtok"}}
	if code, body := e.do(t, "POST", "/api/v1/registries", in, admin); code != http.StatusCreated || body["editable"] != true {
		t.Fatalf("create: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/registries", in, admin); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	bad := map[string]any{"name": "bad", "kind": "fakereg", "url": "reg.example.com/new"}
	if code, _ := e.do(t, "POST", "/api/v1/registries", bad, admin); code != http.StatusBadRequest {
		t.Fatalf("missing credentials: %d", code)
	}

	upd := map[string]any{"kind": "fakereg", "url": "reg.example.com/new2", "credentials": map[string]string{"token": ""}}
	code, body = e.do(t, "PUT", "/api/v1/registries/new", upd, admin)
	if code != http.StatusOK || body["url"] != "reg.example.com/new2" {
		t.Fatalf("update: %d %v", code, body)
	}
	if r, _ := e.regs.Get(context.Background(), "new"); r.Credentials["token"] != "regtok" {
		t.Fatalf("blank credential should keep the stored value: %v", r.Credentials)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/registries/decl", upd, admin); code != http.StatusConflict {
		t.Fatalf("update declarative: %d", code)
	}

	code, body = e.do(t, "POST", "/api/v1/registries/new/test", nil, admin)
	if code != http.StatusOK || body["ok"] != true || body["repositoryCount"] != float64(3) {
		t.Fatalf("test: %d %v", code, body)
	}
	draft := map[string]any{"name": "new", "kind": "fakereg", "url": "reg.example.com/x", "credentials": map[string]string{"token": "wrong"}}
	if code, body := e.do(t, "POST", "/api/v1/test-registry", draft, admin); code != http.StatusOK || body["ok"] != false {
		t.Fatalf("draft test: %d %v", code, body)
	}

	code, body = e.do(t, "DELETE", "/api/v1/registries/do", nil, admin)
	if code != http.StatusConflict || !strings.Contains(body["error"].(string), "shared") {
		t.Fatalf("delete in use: %d %v", code, body)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/registries/new", nil, admin); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/registries/new", nil, admin); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
}

func TestConnectionImages(t *testing.T) {
	e := newEnv(false)
	code, body := e.do(t, "GET", "/api/v1/connections/shared/images?ref=dev", nil)
	if code != http.StatusOK || body["configured"] != true || body["branch"] != "dev" {
		t.Fatalf("images: %d %v", code, body)
	}
	repos := body["repositories"].([]any)
	if len(repos) != 1 {
		t.Fatalf("branch filter: %s", mustJSON(body))
	}
	repo := repos[0].(map[string]any)
	tag := repo["tags"].([]any)[1].(map[string]any)
	if repo["image"] != "reg.example.com/team/web-dev" || tag["commitURL"] != "https://x/o/r/commit/1111111" || tag["image"] != "reg.example.com/team/web-dev:1111111" {
		t.Fatalf("repo: %s", mustJSON(repo))
	}
	if strings.Contains(mustJSON(body), "regtok") {
		t.Fatal("images response leaks registry credentials")
	}

	_, body = e.do(t, "GET", "/api/v1/connections/shared/images", nil)
	if len(body["repositories"].([]any)) != 2 {
		t.Fatalf("all branches: %s", mustJSON(body))
	}
	if code, body := e.do(t, "GET", "/api/v1/connections/gitops/images", nil); code != http.StatusOK || body["configured"] != false {
		t.Fatalf("not configured: %d %v", code, body)
	}
	if code, _ := e.do(t, "GET", "/api/v1/connections/secret/images", nil); code != http.StatusNotFound {
		t.Fatalf("inaccessible connection: %d", code)
	}
}

func TestConnectionImageSourcesValidation(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	in := map[string]any{"name": "img", "provider": "fake", "url": "https://x/o/i", "credentials": map[string]string{"token": "t"},
		"images": []map[string]string{{"registry": "missing", "repository": ".*"}}}
	code, body := e.do(t, "POST", "/api/v1/connections", in, admin)
	if code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "does not exist") {
		t.Fatalf("missing registry: %d %v", code, body)
	}
	in["images"] = []map[string]string{{"registry": "do", "repository": "("}}
	if code, _ := e.do(t, "POST", "/api/v1/connections", in, admin); code != http.StatusBadRequest {
		t.Fatalf("bad regex: %d", code)
	}
	in["images"] = []map[string]string{{"registry": "do", "repository": "^web-", "tags": "^[0-9]+$"}}
	code, body = e.do(t, "POST", "/api/v1/connections", in, admin)
	if code != http.StatusCreated || mustJSON(body["images"]) != `[{"registry":"do","repository":"^web-","tags":"^[0-9]+$"}]` {
		t.Fatalf("create with images: %d %v", code, body)
	}

	preview := map[string]any{"images": []map[string]string{{"registry": "do", "repository": "^web-(?P<branch>.+)$"}}, "ref": "main"}
	code, body = e.do(t, "POST", "/api/v1/images/preview", preview, admin)
	if code != http.StatusOK || len(body["repositories"].([]any)) != 1 {
		t.Fatalf("preview: %d %v", code, body)
	}
}
