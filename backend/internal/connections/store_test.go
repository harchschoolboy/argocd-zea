package connections

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const ns = "zea-connections"

func declarative() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "any-name", Namespace: ns,
			Labels: map[string]string{LabelSecretType: SecretTypeConnection},
		},
		Data: map[string][]byte{
			KeyName: []byte("gitops"), KeyProvider: []byte("gitlab"), KeyURL: []byte("https://gitlab.com/g/p"),
			KeyAllowedGroups: []byte("devs, ops"), "token": []byte("glpat"),
		},
	}
}

func unrelated() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns}, Data: map[string][]byte{KeyName: []byte("x")}}
}

func TestSecretStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewSecretStore(fake.NewClientset(declarative(), unrelated()), ns)

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	g := list[0]
	if g.Name != "gitops" || g.Editable || g.Credentials["token"] != "glpat" || len(g.AllowedGroups) != 2 || g.AllowedGroups[1] != "ops" {
		t.Fatalf("declarative mapping wrong: %+v", g)
	}
	if _, err := s.Update(ctx, g); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("update declarative: %v", err)
	}
	if err := s.Delete(ctx, "gitops"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete declarative: %v", err)
	}

	c := &Connection{Name: "app", Provider: "github", URL: "https://github.com/o/r", AllowedGroups: []string{"*"},
		Credentials: map[string]string{"token": "ghp"}}
	created, err := s.Create(ctx, c)
	if err != nil || !created.Editable || created.SecretName != "zea-conn-app" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := s.Create(ctx, c); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if _, err := s.Create(ctx, &Connection{Name: "gitops"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("create clashing with declarative name: %v", err)
	}

	upd := &Connection{Name: "app", Provider: "github", URL: "https://github.com/o/r2", Credentials: map[string]string{"token": ""},
		Images: []ImageSource{{Registry: "do", Repository: `^app-(?P<branch>.+)$`, Tags: `^[0-9a-f]{7}$`}}}
	got, err := s.Update(ctx, upd)
	if err != nil || got.URL != "https://github.com/o/r2" || got.Credentials["token"] != "ghp" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if len(got.Images) != 1 || got.Images[0] != upd.Images[0] || got.ImagesError != "" || got.Credentials[KeyImages] != "" {
		t.Fatalf("images round trip = %+v %q", got.Images, got.ImagesError)
	}
	got, err = s.Update(ctx, &Connection{Name: "app", Provider: "github", URL: "https://github.com/o/r2"})
	if err != nil || len(got.Images) != 0 {
		t.Fatalf("clearing images = %+v, %v", got, err)
	}

	if err := s.Delete(ctx, "app"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestMergeCredentials(t *testing.T) {
	got := MergeCredentials(map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "", "b": "-", "c": "3"})
	if len(got) != 2 || got["a"] != "1" || got["c"] != "3" {
		t.Fatalf("merge = %v", got)
	}
}

func TestValidate(t *testing.T) {
	ok := &Connection{Name: "my-app", Provider: "github", URL: "https://github.com/o/r"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []*Connection{
		{Name: "My_App", Provider: "github", URL: "https://github.com/o/r"},
		{Name: "-x", Provider: "github", URL: "https://github.com/o/r"},
		{Name: "x", URL: "https://github.com/o/r"},
		{Name: "x", Provider: "github", URL: "git@github.com:o/r"},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", APIURL: "ftp://x"},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", Credentials: map[string]string{"provider": "x"}},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", Credentials: map[string]string{"images": "x"}},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", Images: []ImageSource{{Registry: "do"}}},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", Images: []ImageSource{{Registry: "do", Repository: "("}}},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", Images: []ImageSource{{Registry: "do", Repository: "a", Tags: "["}}},
		{Name: "x", Provider: "github", URL: "https://github.com/o/r", ImagesError: "broken"},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
}

func TestParseImageSources(t *testing.T) {
	yamlSrc := "- registry: do\n  repository: ^web-(?P<branch>.+)$\n  tags: ^[0-9a-f]+$\n"
	jsonSrc := `[{"registry":"do","repository":"^web-(?P<branch>.+)$","tags":"^[0-9a-f]+$"}]`
	for _, raw := range []string{yamlSrc, jsonSrc} {
		got, err := ParseImageSources(raw)
		if err != nil || len(got) != 1 || got[0].Repository != "^web-(?P<branch>.+)$" || got[0].Tags != "^[0-9a-f]+$" {
			t.Fatalf("parse %q = %+v, %v", raw, got, err)
		}
	}
	if _, err := ParseImageSources("- registry: do\n  unknown: 1\n"); err == nil {
		t.Fatal("unknown field should fail")
	}
	if got, err := ParseImageSources("  "); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	back, err := ParseImageSources(FormatImageSources([]ImageSource{{Registry: "r", Repository: `^a:"b'$`}}))
	if err != nil || back[0].Repository != `^a:"b'$` {
		t.Fatalf("format round trip = %+v, %v", back, err)
	}
	sec := ToSecret(&Connection{Name: "a", Provider: "github", URL: "https://github.com/o/r"})
	sec.StringData[KeyImages] = "not: [a list"
	if c := FromSecret(sec); c.ImagesError == "" || c.Credentials[KeyImages] != "" {
		t.Fatalf("broken images = %+v", c)
	}
}
