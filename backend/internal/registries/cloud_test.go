package registries

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func pullSecret(name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: name},
		Data:       map[string][]byte{key: []byte(value)},
	}
}

func TestPullSecrets(t *testing.T) {
	f := newFakeRegistry(t)
	host := strings.TrimPrefix(f.srv.URL, "http://")
	client := fake.NewSimpleClientset(
		pullSecret("regcred", corev1.DockerConfigJsonKey, dockerConfig(host, "user", "pass")),
		pullSecret("legacy", corev1.DockerConfigKey, `{"`+host+`":{"username":"user","password":"pass"}}`),
		pullSecret("empty", "other", "x"),
		pullSecret("other", corev1.DockerConfigJsonKey, dockerConfig(host, "user", "pass")),
	)
	client.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.GetAction).GetName() == "forbidden" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "forbidden", errors.New("no"))
		}
		return false, nil, nil
	})
	src := NewKubePullSecrets(client, []string{"apps/regcred", "apps/legacy", "apps/empty", "apps/missing", "apps/forbidden", "bad ref", "apps/regcred"})
	if got := strings.Join(src.Allowed(), ","); got != "apps/regcred,apps/legacy,apps/empty,apps/missing,apps/forbidden" {
		t.Fatalf("allowed = %s", got)
	}

	ctx := context.Background()
	if cfg, err := src.DockerConfig(ctx, "apps/legacy"); err != nil || !strings.HasPrefix(cfg, `{"auths":`) {
		t.Fatalf("legacy = %s, %v", cfg, err)
	}
	for ref, want := range map[string]string{
		"apps/other":     "not listed",
		"apps/missing":   "does not exist",
		"apps/forbidden": "may not read",
		"apps/empty":     "has no .dockerconfigjson",
		"nope":           "invalid pull secret",
	} {
		if _, err := src.DockerConfig(ctx, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", ref, err)
		}
	}

	k := NewKinds(NewOCI(f.srv.Client()))
	modeOf := func() *struct{ options []string } {
		for _, m := range k.Infos()[0].CredentialModes {
			if m.ID == PullSecretModeID {
				return &struct{ options []string }{m.Fields[0].Options}
			}
		}
		return nil
	}
	if modeOf() != nil {
		t.Fatal("pull secret mode must be hidden without an allowlist")
	}
	reg := &Registry{Name: "r", Kind: "oci", URL: f.srv.URL + "/team", Credentials: map[string]string{CredPullSecret: "apps/regcred"}}
	if err := k.Validate(reg); err == nil {
		t.Fatal("pull secret must be rejected without an allowlist")
	}
	c, _ := k.Get("oci")
	if _, err := c.ListRepositories(ctx, reg); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured: %v", err)
	}

	k.SetPullSecrets(src)
	if m := modeOf(); m == nil || len(m.options) != 5 {
		t.Fatalf("pull secret mode = %+v", m)
	}
	if m := NewOCI(nil).Info().CredentialModes; m[len(m)-1].ID != PullSecretModeID || m[len(m)-1].Fields[0].Options != nil {
		t.Fatal("Infos must not modify the kind's own modes")
	}
	if err := k.Validate(reg); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"apps/other", "bad"} {
		r := *reg
		r.Credentials = map[string]string{CredPullSecret: ref}
		if err := k.Validate(&r); err == nil {
			t.Fatalf("%s should be rejected", ref)
		}
	}

	c, _ = k.Get("oci")
	if _, ok := c.(TagDescriber); !ok {
		t.Fatal("wrapper must keep TagDescriber")
	}
	repos, err := c.ListRepositories(ctx, reg)
	if err != nil || len(repos) != 2 {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	tags, err := c.ListTags(ctx, reg, "web")
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	if desc := c.(TagDescriber).DescribeTags(ctx, reg, "web", tags); desc[0].Digest != "sha256:idx" {
		t.Fatalf("describe = %+v", desc)
	}
	legacy := *reg
	legacy.Credentials = map[string]string{CredPullSecret: "apps/legacy"}
	if _, err := c.ListRepositories(ctx, &legacy); err != nil {
		t.Fatalf("legacy: %v", err)
	}
	missing := *reg
	missing.Credentials = map[string]string{CredPullSecret: "apps/missing"}
	if _, err := c.ListTags(ctx, &missing, "web"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing: %v", err)
	}
}

const garPrefix = "/v1/projects/proj/locations/europe-west1/repositories/repo"

func newFakeGAR(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gtok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
			return
		}
		res := "projects/proj/locations/europe-west1/repositories/repo/packages/"
		switch {
		case r.URL.EscapedPath() == garPrefix+"/packages" && r.URL.Query().Get("pageToken") == "":
			fmt.Fprintf(w, `{"packages":[{"name":"%steam%%2Fweb","updateTime":"2026-01-02T00:00:00Z"},{"name":"%sother"}],"nextPageToken":"p2"}`, res, res)
		case r.URL.EscapedPath() == garPrefix+"/packages" && r.URL.Query().Get("pageToken") == "p2":
			fmt.Fprintf(w, `{"packages":[{"name":"%steam%%2Fapi"}]}`, res)
		case r.URL.EscapedPath() == garPrefix+"/packages/team%2Fweb/versions" && r.URL.Query().Get("view") == "FULL":
			fmt.Fprintf(w, `{"versions":[
				{"name":"%steam%%2Fweb/versions/sha256:aaa","createTime":"2026-01-03T00:00:00Z","updateTime":"2026-01-04T00:00:00Z",
				 "relatedTags":[{"name":"%steam%%2Fweb/tags/latest"},{"name":"%steam%%2Fweb/tags/abc1234"}],
				 "metadata":{"imageSizeBytes":"1234","mediaType":"application/vnd.oci.image.manifest.v1+json"}},
				{"name":"%steam%%2Fweb/versions/sha256:bbb","createTime":"2026-01-01T00:00:00Z"}]}`, res, res, res, res)
		default:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"message":"Permission 'artifactregistry.repositories.downloadArtifacts' denied","status":"PERMISSION_DENIED"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func staticCreds() *google.Credentials {
	return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "gtok"})}
}

func TestGAR(t *testing.T) {
	srv := newFakeGAR(t)
	ctx := context.Background()
	var adcCalls, keyCalls atomic.Int32
	g := NewGAR(srv.Client())
	g.APIBase = srv.URL
	g.FindDefault = func(context.Context, ...string) (*google.Credentials, error) {
		adcCalls.Add(1)
		return staticCreds(), nil
	}
	g.FromKey = func(_ context.Context, key []byte, _ ...string) (*google.Credentials, error) {
		keyCalls.Add(1)
		if string(key) != `{"type":"service_account"}` {
			return nil, errors.New("bad key")
		}
		return staticCreds(), nil
	}

	reg := &Registry{Name: "gar", Kind: "gar", URL: "europe-west1-docker.pkg.dev/proj/repo/team"}
	repos, err := g.ListRepositories(ctx, reg)
	if err != nil || len(repos) != 2 || repos[0].Name != "web" || repos[1].Name != "api" || repos[0].UpdatedAt.IsZero() {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	tags, err := g.ListTags(ctx, reg, "web")
	if err != nil || len(tags) != 2 || tags[0].Name != "latest" || tags[1].Name != "abc1234" || tags[0].Digest != "sha256:aaa" ||
		tags[0].SizeBytes != 1234 || !tags[0].PushedAt.Equal(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	if adcCalls.Load() != 1 {
		t.Fatalf("ADC must be cached, calls = %d", adcCalls.Load())
	}
	root := &Registry{Name: "gar", Kind: "gar", URL: "europe-west1-docker.pkg.dev/proj/repo"}
	if repos, err := g.ListRepositories(ctx, root); err != nil || len(repos) != 3 || repos[0].Name != "team/web" {
		t.Fatalf("root repos = %+v, %v", repos, err)
	}
	if tags, err := g.ListTags(ctx, root, "team/web"); err != nil || len(tags) != 2 {
		t.Fatalf("root tags = %+v, %v", tags, err)
	}
	if _, err := g.ListTags(ctx, reg, "missing"); err == nil || !strings.Contains(err.Error(), "Artifact Registry API returned 403: Permission") {
		t.Fatalf("missing: %v", err)
	}

	key := &Registry{Name: "gar", Kind: "gar", URL: reg.URL, Credentials: map[string]string{CredServiceAccountKey: `{"type":"service_account"}`}}
	if _, err := g.ListTags(ctx, key, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ListTags(ctx, key, "web"); err != nil || keyCalls.Load() != 1 {
		t.Fatalf("key source must be cached: calls=%d err=%v", keyCalls.Load(), err)
	}

	host := "europe-west1-docker.pkg.dev"
	dc := func(user, pass string) *Registry {
		return &Registry{Name: "gar", Kind: "gar", URL: reg.URL, Credentials: map[string]string{CredDockerConfig: dockerConfig(host, user, pass)}}
	}
	for name, r := range map[string]*Registry{
		"json key":     dc("_json_key", `{"type":"service_account"}`),
		"base64 key":   dc("_json_key_base64", "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0="),
		"access token": dc("oauth2accesstoken", "gtok"),
	} {
		if _, err := g.ListTags(ctx, r, "web"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := g.ListTags(ctx, dc("someone", "x"), "web"); err == nil || !strings.Contains(err.Error(), "_json_key") {
		t.Fatalf("unknown docker user: %v", err)
	}
	if _, err := g.ListTags(ctx, dc("oauth2accesstoken", "stale"), "web"); err == nil || !strings.Contains(err.Error(), "invalid authentication") {
		t.Fatalf("stale token: %v", err)
	}

	noADC := NewGAR(srv.Client())
	noADC.APIBase = srv.URL
	noADC.FindDefault = func(context.Context, ...string) (*google.Credentials, error) {
		adcCalls.Add(1)
		return nil, errors.New("could not find default credentials")
	}
	before := adcCalls.Load()
	for i := 0; i < 2; i++ {
		if _, err := noADC.ListRepositories(ctx, reg); err == nil || !strings.Contains(err.Error(), "no Google credentials") {
			t.Fatalf("no ADC: %v", err)
		}
	}
	if adcCalls.Load()-before != 1 {
		t.Fatal("ADC errors must be cached for a while")
	}

	k := NewKinds(g)
	for _, r := range []*Registry{reg, root, key} {
		if err := k.Validate(r); err != nil {
			t.Fatalf("%s: %v", r.URL, err)
		}
	}
	for _, raw := range []string{"gcr.io/proj/repo", "europe-west1-docker.pkg.dev/proj", "http://europe-west1-docker.pkg.dev/proj/repo", "-docker.pkg.dev/proj/repo"} {
		if err := k.Validate(&Registry{Name: "gar", Kind: "gar", URL: raw}); err == nil {
			t.Fatalf("%s should be rejected", raw)
		}
	}
	badKey := &Registry{Name: "gar", Kind: "gar", URL: reg.URL, Credentials: map[string]string{CredServiceAccountKey: "{}"}}
	if err := k.Validate(badKey); err == nil || !strings.Contains(err.Error(), "invalid service account key") {
		t.Fatalf("bad key: %v", err)
	}
}
