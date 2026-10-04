package proxytoken

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func get(t *testing.T, kube *fake.Clientset) *corev1.Secret {
	t.Helper()
	s, err := kube.CoreV1().Secrets("argocd").Get(context.Background(), "zea-proxy", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnsureCreatesAndKeeps(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()

	res, err := Ensure(ctx, kube, "argocd", "zea-proxy", "token")
	if err != nil || res != Created {
		t.Fatalf("first run: %v %v", res, err)
	}
	s := get(t, kube)
	token := string(s.Data["token"])
	if len(token) != 2*tokenBytes {
		t.Fatalf("token length %d", len(token))
	}
	if s.Labels[PartOfLabel] != PartOfValue {
		t.Fatalf("labels %v", s.Labels)
	}

	res, err = Ensure(ctx, kube, "argocd", "zea-proxy", "token")
	if err != nil || res != Unchanged {
		t.Fatalf("second run: %v %v", res, err)
	}
	if got := string(get(t, kube).Data["token"]); got != token {
		t.Fatal("token was replaced")
	}
}

func TestEnsureFixesExisting(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "zea-proxy", Namespace: "argocd", Labels: map[string]string{"x": "y"}},
			Data:       map[string][]byte{"token": []byte("user-provided")},
		},
	)
	res, err := Ensure(ctx, kube, "argocd", "zea-proxy", "token")
	if err != nil || res != Updated {
		t.Fatalf("run: %v %v", res, err)
	}
	s := get(t, kube)
	if string(s.Data["token"]) != "user-provided" {
		t.Fatal("existing token must be kept")
	}
	if s.Labels[PartOfLabel] != PartOfValue || s.Labels["x"] != "y" {
		t.Fatalf("labels %v", s.Labels)
	}

	res, err = Ensure(ctx, kube, "argocd", "zea-proxy", "other")
	if err != nil || res != Updated {
		t.Fatalf("missing key: %v %v", res, err)
	}
	s = get(t, kube)
	if len(s.Data["other"]) == 0 || string(s.Data["token"]) != "user-provided" {
		t.Fatalf("data %v", s.Data)
	}
}

func TestEnsureValidates(t *testing.T) {
	if _, err := Ensure(context.Background(), fake.NewClientset(), "argocd", "", "token"); err == nil {
		t.Fatal("expected error")
	}
}
