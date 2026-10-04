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

	upd := &Connection{Name: "app", Provider: "github", URL: "https://github.com/o/r2", Credentials: map[string]string{"token": ""}}
	got, err := s.Update(ctx, upd)
	if err != nil || got.URL != "https://github.com/o/r2" || got.Credentials["token"] != "ghp" {
		t.Fatalf("update = %+v, %v", got, err)
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
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
}
