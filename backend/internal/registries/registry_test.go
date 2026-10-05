package registries

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

const ns = "zea-connections"

func dockerConfig(host, user, pass string) string {
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return `{"auths":{"` + host + `":{"auth":"` + auth + `"}}}`
}

func TestParseURL(t *testing.T) {
	cases := map[string]Location{
		"registry.digitalocean.com/vmist":  {Scheme: "https", Host: "registry.digitalocean.com", Namespace: "vmist"},
		"https://Harbor.example.com/a/b/":  {Scheme: "https", Host: "harbor.example.com", Namespace: "a/b"},
		"http://localhost:5000":            {Scheme: "http", Host: "localhost:5000"},
		" ghcr.io/harchschoolboy/charts  ": {Scheme: "https", Host: "ghcr.io", Namespace: "harchschoolboy/charts"},
	}
	for raw, want := range cases {
		got, err := ParseURL(raw)
		if err != nil || *got != want {
			t.Fatalf("ParseURL(%q) = %+v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "ftp://x", "https://u:p@x", "x?y=1", "https:///a"} {
		if _, err := ParseURL(raw); err == nil {
			t.Fatalf("ParseURL(%q) should fail", raw)
		}
	}
	loc, _ := ParseURL("registry.digitalocean.com/vmist")
	if loc.ImageRef("web") != "registry.digitalocean.com/vmist/web" || loc.Base() != "https://registry.digitalocean.com" {
		t.Fatalf("refs = %s %s", loc.ImageRef("web"), loc.Base())
	}
}

func TestDockerConfigAuth(t *testing.T) {
	u, p, err := DockerConfigAuth(dockerConfig("https://registry.digitalocean.com", "tok", "tok"), "registry.digitalocean.com")
	if err != nil || u != "tok" || p != "tok" {
		t.Fatalf("auth = %q %q %v", u, p, err)
	}
	u, p, err = DockerConfigAuth(`{"auths":{"https://index.docker.io/v1/":{"username":"me","password":"pw"}}}`, "docker.io")
	if err != nil || u != "me" || p != "pw" {
		t.Fatalf("docker hub = %q %q %v", u, p, err)
	}
	if _, _, err := DockerConfigAuth(dockerConfig("ghcr.io", "a", "b"), "quay.io"); err == nil {
		t.Fatal("missing host should fail")
	}
	if _, _, err := DockerConfigAuth("{", "x"); err == nil {
		t.Fatal("bad json should fail")
	}
	r := &Registry{Credentials: map[string]string{CredToken: "t"}}
	if u, p, ok, _ := r.BasicAuth("x"); !ok || u != "t" || p != "t" {
		t.Fatalf("token basic auth = %q %q %v", u, p, ok)
	}
}

func TestKindsValidate(t *testing.T) {
	k := NewKinds(NewDigitalOcean(nil), NewOCI(nil))
	ok := []*Registry{
		{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist", Credentials: map[string]string{CredToken: "t"}},
		{Name: "do2", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist",
			Credentials: map[string]string{CredDockerConfig: dockerConfig("registry.digitalocean.com", "t", "t")}},
		{Name: "anon", Kind: "oci", URL: "localhost:5000"},
		{Name: "basic", Kind: "oci", URL: "harbor.example.com/team", Credentials: map[string]string{CredUsername: "u", CredPassword: "p"}},
	}
	for _, r := range ok {
		if err := k.Validate(r); err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
	}
	bad := []*Registry{
		{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/vmist"},
		{Name: "do", Kind: "digitalocean", URL: "ghcr.io/vmist", Credentials: map[string]string{CredToken: "t"}},
		{Name: "do", Kind: "digitalocean", URL: "registry.digitalocean.com/a/b", Credentials: map[string]string{CredToken: "t"}},
		{Name: "x", Kind: "nope", URL: "x.io"},
		{Name: "X", Kind: "oci", URL: "x.io"},
		{Name: "x", Kind: "oci", URL: "x.io", Credentials: map[string]string{CredUsername: "u"}},
		{Name: "x", Kind: "oci", URL: "x.io", Credentials: map[string]string{"other": "u"}},
		{Name: "x", Kind: "oci", URL: "x.io", Credentials: map[string]string{CredDockerConfig: dockerConfig("y.io", "a", "b")}},
		{Name: "x", Kind: "oci", URL: "x.io/Bad_NS"},
	}
	for i, r := range bad {
		if err := k.Validate(r); err == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
}

func TestSecretStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	declarative := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "do-pull", Namespace: ns,
			Labels: map[string]string{connections.LabelSecretType: SecretTypeRegistry}},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			KeyName: []byte("do"), KeyKind: []byte("digitalocean"), KeyURL: []byte("registry.digitalocean.com/vmist"),
			CredDockerConfig: []byte(dockerConfig("registry.digitalocean.com", "t", "t")),
		},
	}
	conn := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: ns,
			Labels: map[string]string{connections.LabelSecretType: connections.SecretTypeConnection}},
		Data: map[string][]byte{KeyName: []byte("conn")},
	}
	s := NewSecretStore(fake.NewClientset(declarative, conn), ns)

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "do" || list[0].Editable || list[0].Credentials[CredDockerConfig] == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.Delete(ctx, "do"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete declarative: %v", err)
	}

	r := &Registry{Name: "harbor", Kind: "oci", URL: "harbor.example.com/team", Credentials: map[string]string{CredUsername: "u", CredPassword: "p"}}
	created, err := s.Create(ctx, r)
	if err != nil || !created.Editable || created.SecretName != "zea-registry-harbor" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := s.Create(ctx, &Registry{Name: "do"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	got, err := s.Update(ctx, &Registry{Name: "harbor", Kind: "oci", URL: "harbor.example.com/other",
		Credentials: map[string]string{CredUsername: "", CredPassword: "p2"}})
	if err != nil || got.URL != "harbor.example.com/other" || got.Credentials[CredUsername] != "u" || got.Credentials[CredPassword] != "p2" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if err := s.Delete(ctx, "harbor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "harbor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}
