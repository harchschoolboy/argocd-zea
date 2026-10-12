package argocd

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

func testApp() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "web", "namespace": "argocd"},
		"status": map[string]any{
			"sync":    map[string]any{"status": "Synced", "revision": "abc123"},
			"health":  map[string]any{"status": "Degraded", "message": "crash loop"},
			"summary": map[string]any{"images": []any{"reg.io/web:v2", "redis:7"}},
			"operationState": map[string]any{
				"phase":      "Succeeded",
				"message":    "successfully synced",
				"finishedAt": "2026-01-02T03:04:05Z",
			},
		},
	}}
}

func TestApps(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{ApplicationResource: "ApplicationList"}, testApp())
	apps := NewApps(client)
	ctx := context.Background()

	st, err := apps.GetApp(ctx, "argocd", "web")
	if err != nil {
		t.Fatal(err)
	}
	want := streams.AppState{
		Sync: "Synced", Health: "Degraded", HealthMessage: "crash loop", Revision: "abc123",
		Images: []string{"reg.io/web:v2", "redis:7"}, OperationPhase: "Succeeded", OperationMessage: "successfully synced",
		OperationFinishedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if st.Sync != want.Sync || st.Health != want.Health || st.HealthMessage != want.HealthMessage || st.Revision != want.Revision ||
		len(st.Images) != 2 || st.OperationPhase != want.OperationPhase || st.OperationMessage != want.OperationMessage ||
		!st.OperationFinishedAt.Equal(want.OperationFinishedAt) {
		t.Fatalf("state = %+v", st)
	}

	if err := apps.RefreshApp(ctx, "argocd", "web"); err != nil {
		t.Fatal(err)
	}
	obj, err := client.Resource(ApplicationResource).Namespace("argocd").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAnnotations()["argocd.argoproj.io/refresh"] != "normal" {
		t.Fatalf("annotations = %v", obj.GetAnnotations())
	}

	if _, err := apps.GetApp(ctx, "argocd", "nope"); !errors.Is(err, streams.ErrAppNotFound) {
		t.Fatalf("missing app: %v", err)
	}
	if err := apps.RefreshApp(ctx, "other", "web"); !errors.Is(err, streams.ErrAppNotFound) {
		t.Fatalf("refresh missing app: %v", err)
	}
}

func TestAppStateOfMultiSource(t *testing.T) {
	st := AppStateOf(map[string]any{"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revisions": []any{"r1", "r2"}}}})
	if st.Revision != "r1" || st.Sync != "OutOfSync" || !st.OperationFinishedAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}
