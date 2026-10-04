package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

func TestHasWorkflowDispatch(t *testing.T) {
	cases := map[string]bool{
		"on: workflow_dispatch":         true,
		"on: push":                      false,
		"on: [push, workflow_dispatch]": true,
		"on:\n  push:\n  workflow_dispatch:\n    inputs: {}":     true,
		"on:\n  push:\n    branches: [main]":                     false,
		"name: x\njobs: {}":                                      false,
		"\"on\":\n  workflow_dispatch:":                          true,
		"x: &t workflow_dispatch\non: *t":                        true,
		"on:\n  pull_request:\n    types: [workflow_dispatch]\n": false,
	}
	for src, want := range cases {
		got, err := hasWorkflowDispatch([]byte(src))
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", src, got, err, want)
		}
	}
	if _, err := hasWorkflowDispatch([]byte("on: [")); err == nil {
		t.Error("expected parse error")
	}
}

func TestResolve(t *testing.T) {
	cases := []struct{ url, api, wantBase, wantOwner, wantRepo string }{
		{"https://github.com/o/r", "", "https://api.github.com", "o", "r"},
		{"https://github.com/o/r.git", "", "https://api.github.com", "o", "r"},
		{"https://ghe.corp/o/r/", "", "https://ghe.corp/api/v3", "o", "r"},
		{"https://ghe.corp/o/r", "https://api.ghe.corp/", "https://api.ghe.corp", "o", "r"},
	}
	for _, c := range cases {
		got, err := resolve(&connections.Connection{URL: c.url, APIURL: c.api})
		if err != nil || got.apiBase != c.wantBase || got.owner != c.wantOwner || got.repo != c.wantRepo {
			t.Errorf("%s: got %+v, %v", c.url, got, err)
		}
	}
	for _, bad := range []string{"https://github.com/o", "https://github.com/o/r/tree/main", "https://user:pw@github.com/o/r"} {
		if _, err := resolve(&connections.Connection{URL: bad}); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
}

func pemKey(t *testing.T, k *rsa.PrivateKey, pkcs8 bool) string {
	t.Helper()
	if pkcs8 {
		b, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func TestParsePrivateKey(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{pemKey(t, k, false), pemKey(t, k, true), strings.ReplaceAll(pemKey(t, k, false), "\n", `\n`)} {
		if _, err := parsePrivateKey(s); err != nil {
			t.Errorf("parse failed: %v", err)
		}
	}
	if _, err := parsePrivateKey("nope"); err == nil {
		t.Error("expected error")
	}
}

// fakeGitHub serves the subset of the GitHub API used by the provider.
type fakeGitHub struct {
	t           *testing.T
	key         *rsa.PublicKey
	tokenCalls  atomic.Int32
	writeTokens atomic.Int32
	dispatched  map[string]any
	posted      []string
	srv         *httptest.Server
}

func newFakeGitHub(t *testing.T, key *rsa.PublicKey) *fakeGitHub {
	f := &fakeGitHub{t: t, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/app/installations/42/access_tokens", f.accessToken)
	mux.HandleFunc("GET /api/v3/repos/o/r", f.auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"full_name":"o/r","default_branch":"main","html_url":"https://x/o/r"}`)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/branches", f.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Link", `<https://evil.example/steal>; rel="next"`)
			fmt.Fprint(w, `[{"name":"dev","commit":{"sha":"b"}}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/o/r/branches?per_page=100&page=2>; rel="next", <x>; rel="last"`, f.srv.URL))
		fmt.Fprint(w, `[{"name":"main","protected":true,"commit":{"sha":"a"}}]`)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/workflows", f.auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"workflows":[
			{"id":1,"name":"Build","path":".github/workflows/build.yml","state":"active"},
			{"id":2,"name":"Lint","path":".github/workflows/lint.yml","state":"active"},
			{"id":3,"name":"Old","path":".github/workflows/old.yml","state":"disabled_manually"},
			{"id":4,"name":"Pages","path":"dynamic/pages/pages-build-deployment","state":"active"},
			{"id":5,"name":"New","path":".github/workflows/new.yml","state":"active"}]}`)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/contents/.github/workflows/{file}", f.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Errorf("contents Accept = %q", r.Header.Get("Accept"))
		}
		if r.URL.Query().Get("ref") != "feature/x" {
			t.Errorf("contents ref = %q", r.URL.Query().Get("ref"))
		}
		switch r.PathValue("file") {
		case "build.yml":
			fmt.Fprint(w, buildWorkflow)
		case "lint.yml":
			fmt.Fprint(w, "on: push\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	f.runRoutes(mux)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghs_install" || r.Header.Get("X-GitHub-Api-Version") != apiVersion {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		next(w, r)
	}
}

func (f *fakeGitHub) accessToken(w http.ResponseWriter, r *http.Request) {
	f.tokenCalls.Add(1)
	jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		f.t.Fatalf("bad jwt %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.key, crypto.SHA256, sum[:], sig); err != nil {
		f.t.Fatalf("jwt signature: %v", err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	_ = json.Unmarshal(claims, &c)
	if c.Iss != "123" || c.Exp-c.Iat > 600 {
		f.t.Fatalf("bad claims %+v", c)
	}
	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Permissions["actions"] == "write" {
		f.writeTokens.Add(1)
	}
	if len(body.Repositories) != 1 || body.Repositories[0] != "r" || body.Permissions["contents"] != "read" || (body.Permissions["actions"] != "read" && body.Permissions["actions"] != "write") {
		f.t.Fatalf("bad token request %+v", body)
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"token":"ghs_install","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
}

func appConnection(t *testing.T, srvURL string, key *rsa.PrivateKey) *connections.Connection {
	return &connections.Connection{
		Name: "c", Provider: ID, URL: srvURL + "/o/r",
		Credentials: map[string]string{KeyAppID: "123", KeyInstallationID: "42", KeyPrivateKey: pemKey(t, key, false)},
	}
}

func TestGitHubAppFlow(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeGitHub(t, &key.PublicKey)
	p := New(f.srv.Client())
	c := appConnection(t, f.srv.URL, key)
	ctx := context.Background()

	if err := p.Validate(c); err != nil {
		t.Fatalf("validate: %v", err)
	}

	repo, err := p.Test(ctx, c)
	if err != nil || repo.DefaultBranch != "main" {
		t.Fatalf("test: %+v %v", repo, err)
	}

	bl, err := p.ListBranches(ctx, c)
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if bl.DefaultBranch != "main" || len(bl.Branches) != 2 || !bl.Branches[0].Protected || bl.Branches[1].Name != "dev" {
		t.Fatalf("branches = %+v", bl)
	}

	pls, err := p.ListPipelines(ctx, c, "feature/x")
	if err != nil {
		t.Fatalf("pipelines: %v", err)
	}
	got := map[string]string{}
	for _, pl := range pls {
		got[pl.Name] = fmt.Sprintf("%v|%s", pl.Dispatchable, pl.Reason)
	}
	want := map[string]string{
		"Build": "true|",
		"Lint":  "false|workflow has no workflow_dispatch trigger on feature/x",
		"Old":   "false|workflow is disabled_manually",
		"New":   "false|workflow file does not exist on feature/x",
	}
	if len(got) != len(want) {
		t.Fatalf("pipelines = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	if n := f.tokenCalls.Load(); n != 1 {
		t.Fatalf("installation token requested %d times, want 1 (cached)", n)
	}
}

func TestGitHubBadToken(t *testing.T) {
	f := newFakeGitHub(t, nil)
	p := New(f.srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: f.srv.URL + "/o/r", Credentials: map[string]string{KeyToken: "wrong"}}
	_, err := p.Test(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateCredentials(t *testing.T) {
	p := New(http.DefaultClient)
	base := connections.Connection{Name: "c", URL: "https://github.com/o/r"}
	bad := []map[string]string{
		{},
		{KeyAppID: "1"},
		{KeyAppID: "1", KeyInstallationID: "x", KeyPrivateKey: "k"},
		{KeyAppID: "1", KeyInstallationID: "2", KeyPrivateKey: "not pem"},
		{KeyToken: "t", KeyAppID: "1"},
		{"password": "x"},
	}
	for i, creds := range bad {
		c := base
		c.Credentials = creds
		if p.Validate(&c) == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	c := base
	c.Credentials = map[string]string{KeyToken: "t"}
	if err := p.Validate(&c); err != nil {
		t.Errorf("token mode: %v", err)
	}
}
