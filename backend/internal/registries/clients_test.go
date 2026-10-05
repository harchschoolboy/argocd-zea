package registries

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDigitalOcean(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dotok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"id":"unauthorized","message":"Unable to authenticate you"}`)
			return
		}
		switch {
		case r.URL.Path == "/v2/registry/vmist/repositoriesV2" && r.URL.Query().Get("page_token") == "":
			fmt.Fprintf(w, `{"repositories":[{"name":"web-master-prod","tag_count":2,"latest_manifest":{"updated_at":"2026-01-02T00:00:00Z"}}],
				"links":{"pages":{"next":"%s/v2/registry/vmist/repositoriesV2?page=2&page_token=abc&per_page=100"}}}`, srv.URL)
		case r.URL.Path == "/v2/registry/vmist/repositoriesV2":
			fmt.Fprint(w, `{"repositories":[{"name":"web-dev-prod","tag_count":1}],"links":{"pages":{"next":"https://evil.example.com/x"}}}`)
		case r.URL.EscapedPath() == "/v2/registry/vmist/repositories/team%2Fweb/tags":
			fmt.Fprint(w, `{"tags":[{"tag":"abc1234","manifest_digest":"sha256:1","compressed_size_bytes":100,"size_bytes":200,"updated_at":"2026-01-03T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := NewDigitalOcean(srv.Client())
	d.APIBase = srv.URL
	reg := &Registry{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist", Credentials: map[string]string{CredToken: "dotok"}}
	repos, err := d.ListRepositories(context.Background(), reg)
	if err != nil || len(repos) != 2 || repos[0].Name != "web-master-prod" || repos[0].TagCount != 2 || repos[0].UpdatedAt.IsZero() {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	tags, err := d.ListTags(context.Background(), reg, "team/web")
	if err != nil || len(tags) != 1 || tags[0].Name != "abc1234" || tags[0].SizeBytes != 100 || tags[0].Digest != "sha256:1" ||
		!tags[0].PushedAt.Equal(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("tags = %+v, %v", tags, err)
	}

	dc := &Registry{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist",
		Credentials: map[string]string{CredDockerConfig: dockerConfig("registry.digitalocean.com", "dotok", "dotok")}}
	if _, err := d.ListRepositories(context.Background(), dc); err != nil {
		t.Fatalf("docker config token: %v", err)
	}
	bad := &Registry{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist", Credentials: map[string]string{CredToken: "nope"}}
	if _, err := d.ListRepositories(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "Unable to authenticate") {
		t.Fatalf("bad token: %v", err)
	}
}

// fakeRegistry is a distribution API with docker token auth.
type fakeRegistry struct {
	srv       *httptest.Server
	tokenHits atomic.Int32
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{}
	created := map[string]string{"sha256:cfg1": "2026-02-01T10:00:00Z", "sha256:cfg2": "2026-03-01T10:00:00Z"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.tokenHits.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"token": "tok:" + r.URL.Query().Get("scope"), "expires_in": 300})
			return
		}
		scope := "registry:catalog:*"
		if strings.HasPrefix(r.URL.Path, "/v2/team/") {
			scope = "repository:" + strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v2/"), "/", 3)[0] + "/" +
				strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v2/"), "/", 3)[1] + ":pull"
		}
		if r.Header.Get("Authorization") != "Bearer tok:"+scope {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="%s"`, f.srv.URL, scope))
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
			return
		}
		switch r.URL.Path {
		case "/v2/_catalog":
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/_catalog?last=other%2Fx&n=1000>; rel="next"`)
				fmt.Fprint(w, `{"repositories":["other/x","team/api"]}`)
				return
			}
			fmt.Fprint(w, `{"repositories":["team/web"]}`)
		case "/v2/team/web/tags/list":
			fmt.Fprint(w, `{"name":"team/web","tags":["a1b2c3d","latest"]}`)
		case "/v2/team/web/manifests/a1b2c3d":
			w.Header().Set("Docker-Content-Digest", "sha256:idx")
			fmt.Fprintf(w, `{"mediaType":"%s","manifests":[{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}}]}`, mediaOCIIndex)
		case "/v2/team/web/manifests/sha256:amd":
			fmt.Fprint(w, `{"config":{"digest":"sha256:cfg1","size":10},"layers":[{"size":100},{"size":50}]}`)
		case "/v2/team/web/manifests/latest":
			w.Header().Set("Docker-Content-Digest", "sha256:m2")
			fmt.Fprint(w, `{"config":{"digest":"sha256:cfg2","size":5},"layers":[{"size":5}]}`)
		case "/v2/team/web/blobs/sha256:cfg1", "/v2/team/web/blobs/sha256:cfg2":
			fmt.Fprintf(w, `{"created":"%s"}`, created[strings.TrimPrefix(r.URL.Path, "/v2/team/web/blobs/")])
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known"}]}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestOCI(t *testing.T) {
	f := newFakeRegistry(t)
	o := NewOCI(f.srv.Client())
	reg := &Registry{Name: "r", Kind: "oci", URL: f.srv.URL + "/team", Credentials: map[string]string{CredUsername: "user", CredPassword: "pass"}}
	ctx := context.Background()

	repos, err := o.ListRepositories(ctx, reg)
	if err != nil || len(repos) != 2 || repos[0].Name != "api" || repos[1].Name != "web" {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	if _, err := o.ListRepositories(ctx, reg); err != nil || f.tokenHits.Load() != 1 {
		t.Fatalf("token should be cached: hits=%d err=%v", f.tokenHits.Load(), err)
	}
	tags, err := o.ListTags(ctx, reg, "web")
	if err != nil || len(tags) != 2 || tags[0].Name != "a1b2c3d" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	desc := o.DescribeTags(ctx, reg, "web", tags)
	if desc[0].Digest != "sha256:idx" || desc[0].SizeBytes != 160 || !desc[0].PushedAt.Equal(time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("index tag = %+v", desc[0])
	}
	if desc[1].Digest != "sha256:m2" || desc[1].SizeBytes != 10 || desc[1].PushedAt.IsZero() {
		t.Fatalf("plain tag = %+v", desc[1])
	}

	if _, err := o.ListTags(ctx, reg, "missing"); err == nil || !strings.Contains(err.Error(), "repository name not known") {
		t.Fatalf("missing repo: %v", err)
	}
	if _, err := o.ListTags(ctx, reg, "../x"); err == nil {
		t.Fatal("invalid repo name should fail")
	}
	anon := &Registry{Name: "anon", Kind: "oci", URL: f.srv.URL}
	if _, err := o.ListRepositories(ctx, anon); err == nil {
		t.Fatal("anonymous access should be rejected by the token endpoint")
	}
}

func TestParseChallenge(t *testing.T) {
	s, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:a/b:pull,push"`)
	if s != "bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:a/b:pull,push" {
		t.Fatalf("challenge = %s %v", s, p)
	}
	if s, p := parseChallenge(`Basic realm=Registry`); s != "basic" || p["realm"] != "Registry" {
		t.Fatalf("basic = %s %v", s, p)
	}
}
