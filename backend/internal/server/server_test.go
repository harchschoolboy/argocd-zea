package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/config"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

const anchor = "argocd:zea"

type fakeProvider struct{}

func (fakeProvider) Info() providers.Info {
	return providers.Info{ID: "fake", Name: "Fake", CredentialModes: []providers.CredentialMode{
		{ID: "token", Fields: []providers.CredentialField{{Key: "token", Secret: true}}},
	}}
}

func (p fakeProvider) Validate(c *connections.Connection) error {
	_, err := providers.ValidateCredentials(p.Info().CredentialModes, c.Credentials)
	return err
}

func (fakeProvider) Test(_ context.Context, c *connections.Connection) (*providers.Repository, error) {
	if c.Credentials["token"] != "good" {
		return nil, &providers.UpstreamError{Provider: "Fake", Status: 401, Message: "bad token"}
	}
	return &providers.Repository{FullName: "o/r", DefaultBranch: "main"}, nil
}

func (fakeProvider) ListBranches(context.Context, *connections.Connection) (*providers.BranchList, error) {
	return &providers.BranchList{DefaultBranch: "main", Branches: []providers.Branch{{Name: "main"}}}, nil
}

func (fakeProvider) ListPipelines(_ context.Context, _ *connections.Connection, ref string) ([]providers.Pipeline, error) {
	return []providers.Pipeline{{ID: "1", Name: "build@" + ref, Dispatchable: true}}, nil
}

type testEnv struct {
	h     http.Handler
	store *connections.MemoryStore
}

func newEnv(skip bool) *testEnv {
	cfg := &config.Config{ProxyToken: "s3cret", InsecureSkipProxyAuth: skip, AnchorApp: anchor}
	store := connections.NewMemoryStore(
		&connections.Connection{Name: "shared", Provider: "fake", URL: "https://x/o/r", AllowedGroups: []string{"devs"},
			Credentials: map[string]string{"token": "good"}, Editable: true, SecretName: "zea-conn-shared"},
		&connections.Connection{Name: "secret", Provider: "fake", URL: "https://x/o/s", AllowedGroups: []string{"ops"},
			Credentials: map[string]string{"token": "good"}, Editable: true},
		&connections.Connection{Name: "gitops", Provider: "fake", URL: "https://x/o/g", AllowedGroups: []string{"*"},
			Credentials: map[string]string{"token": "good"}, Editable: false},
	)
	deps := Deps{
		Store:     store,
		Providers: providers.NewRegistry(fakeProvider{}),
		Authz:     authz.New([]string{"admin"}, []string{"zea-admins"}),
	}
	return &testEnv{h: New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), deps), store: store}
}

type reqOpt func(*http.Request)

func as(user, groups string) reqOpt {
	return func(r *http.Request) {
		r.Header.Set(argocd.HeaderUsername, user)
		r.Header.Set(argocd.HeaderUserGroups, groups)
	}
}

func withApp(app string) reqOpt {
	return func(r *http.Request) { r.Header.Set(argocd.HeaderApplicationName, app) }
}

func withToken(tok string) reqOpt {
	return func(r *http.Request) {
		if tok == "" {
			r.Header.Del(HeaderProxyToken)
		} else {
			r.Header.Set(HeaderProxyToken, tok)
		}
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, opts ...reqOpt) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(HeaderProxyToken, "s3cret")
	r.Header.Set(argocd.HeaderApplicationName, anchor)
	r.Header.Set(argocd.HeaderProjectName, "default")
	as("alice", "devs")(r)
	for _, o := range opts {
		o(r)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestHealthIsPublic(t *testing.T) {
	rec := httptest.NewRecorder()
	newEnv(false).h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
}

func TestRequiresProxyToken(t *testing.T) {
	e := newEnv(false)
	for _, tok := range []string{"", "wrong"} {
		if code, _ := e.do(t, "GET", "/api/v1/me", nil, withToken(tok)); code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", tok, code)
		}
	}
}

func TestSkipProxyAuth(t *testing.T) {
	if code, _ := newEnv(true).do(t, "GET", "/api/v1/me", nil, withToken("")); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestRequiresAppHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.Header.Set(HeaderProxyToken, "s3cret")
	rec := httptest.NewRecorder()
	newEnv(false).h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRequiresAnchorApp(t *testing.T) {
	if code, _ := newEnv(false).do(t, "GET", "/api/v1/me", nil, withApp("argocd:other")); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestMe(t *testing.T) {
	e := newEnv(false)
	_, body := e.do(t, "GET", "/api/v1/me", nil)
	if body["username"] != "alice" || body["isAdmin"] != false {
		t.Fatalf("unexpected body: %v", body)
	}
	_, body = e.do(t, "GET", "/api/v1/me", nil, as("admin", ""))
	if body["isAdmin"] != true {
		t.Fatalf("admin user not recognized: %v", body)
	}
	_, body = e.do(t, "GET", "/api/v1/me", nil, as("bob", "x,zea-admins"))
	if body["isAdmin"] != true {
		t.Fatalf("admin group not recognized: %v", body)
	}
}

func names(body map[string]any) []string {
	out := []string{}
	for _, c := range body["connections"].([]any) {
		out = append(out, c.(map[string]any)["name"].(string))
	}
	return out
}

func TestListFiltersByGroupsAndHidesCredentials(t *testing.T) {
	e := newEnv(false)
	_, body := e.do(t, "GET", "/api/v1/connections", nil)
	if got := strings.Join(names(body), ","); got != "gitops,shared" {
		t.Fatalf("alice sees %q", got)
	}
	if strings.Contains(mustJSON(body), "good") || strings.Contains(mustJSON(body), "credentialKeys") {
		t.Fatalf("non-admin response leaks credentials: %s", mustJSON(body))
	}
	_, body = e.do(t, "GET", "/api/v1/connections", nil, as("admin", ""))
	if got := strings.Join(names(body), ","); got != "gitops,secret,shared" {
		t.Fatalf("admin sees %q", got)
	}
	if strings.Contains(mustJSON(body), "good") {
		t.Fatalf("admin response leaks credential values")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestInaccessibleConnectionIsNotFound(t *testing.T) {
	if code, _ := newEnv(false).do(t, "GET", "/api/v1/connections/secret/branches", nil); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestBranchesAndPipelines(t *testing.T) {
	e := newEnv(false)
	code, body := e.do(t, "GET", "/api/v1/connections/shared/branches", nil)
	if code != http.StatusOK || body["defaultBranch"] != "main" {
		t.Fatalf("branches: %d %v", code, body)
	}
	code, body = e.do(t, "GET", "/api/v1/connections/gitops/pipelines?ref=dev", nil)
	if code != http.StatusOK || !strings.Contains(mustJSON(body), "build@dev") {
		t.Fatalf("pipelines: %d %v", code, body)
	}
}

func TestOnlyAdminsManageConnections(t *testing.T) {
	e := newEnv(false)
	in := map[string]any{"name": "new", "provider": "fake", "url": "https://x/o/n", "credentials": map[string]string{"token": "t"}}
	if code, _ := e.do(t, "POST", "/api/v1/connections", in); code != http.StatusForbidden {
		t.Fatalf("create by non-admin: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/shared", nil); code != http.StatusForbidden {
		t.Fatalf("delete by non-admin: %d", code)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	in := map[string]any{"name": "new", "provider": "fake", "url": "https://x/o/n", "allowedGroups": []string{"devs"},
		"credentials": map[string]string{"token": "t1"}}
	if code, body := e.do(t, "POST", "/api/v1/connections", in, admin); code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/connections", in, admin); code != http.StatusConflict {
		t.Fatalf("duplicate create: %d", code)
	}

	upd := map[string]any{"provider": "fake", "url": "https://x/o/n2", "allowedGroups": []string{"ops"}, "credentials": map[string]string{"token": ""}}
	if code, body := e.do(t, "PUT", "/api/v1/connections/new", upd, admin); code != http.StatusOK {
		t.Fatalf("update: %d %v", code, body)
	}
	c, _ := e.store.Get(context.Background(), "new")
	if c.URL != "https://x/o/n2" || c.Credentials["token"] != "t1" {
		t.Fatalf("update did not keep credentials: %+v", c)
	}

	if code, _ := e.do(t, "DELETE", "/api/v1/connections/new", nil, admin); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/gitops", nil, admin); code != http.StatusConflict {
		t.Fatalf("delete declarative: %d", code)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	cases := []map[string]any{
		{"name": "Bad Name", "provider": "fake", "url": "https://x/o/n", "credentials": map[string]string{"token": "t"}},
		{"name": "ok", "provider": "nope", "url": "https://x/o/n", "credentials": map[string]string{"token": "t"}},
		{"name": "ok", "provider": "fake", "url": "ftp://x", "credentials": map[string]string{"token": "t"}},
		{"name": "ok", "provider": "fake", "url": "https://x/o/n"},
		{"name": "ok", "provider": "fake", "url": "https://x/o/n", "credentials": map[string]string{"url": "x"}},
		{"name": "ok", "provider": "fake", "url": "https://x/o/n", "unknown": true},
	}
	for i, in := range cases {
		if code, body := e.do(t, "POST", "/api/v1/connections", in, admin); code != http.StatusBadRequest {
			t.Fatalf("case %d: %d %v", i, code, body)
		}
	}
}

func TestTestConnection(t *testing.T) {
	e := newEnv(false)
	_, body := e.do(t, "POST", "/api/v1/connections/shared/test", nil)
	if body["ok"] != true {
		t.Fatalf("saved test: %v", body)
	}
	draft := map[string]any{"name": "shared", "provider": "fake", "url": "https://x/o/r", "credentials": map[string]string{}}
	_, body = e.do(t, "POST", "/api/v1/test-connection", draft, as("admin", ""))
	if body["ok"] != true {
		t.Fatalf("draft test should reuse stored token: %v", body)
	}
	draft["credentials"] = map[string]string{"token": "bad"}
	_, body = e.do(t, "POST", "/api/v1/test-connection", draft, as("admin", ""))
	if body["ok"] != false || !strings.Contains(body["error"].(string), "bad token") {
		t.Fatalf("draft test with bad token: %v", body)
	}
}
