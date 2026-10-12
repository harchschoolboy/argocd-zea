package argocd

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// ApplicationResource is the Argo CD Application custom resource.
var ApplicationResource = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// refreshPatch asks the application controller to compare the Application
// with git now ("normal" refresh, same as the UI's Refresh button).
var refreshPatch = []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"normal"}}}`)

// Apps reads Applications through the Kubernetes API; it satisfies
// streams.AppClient.
type Apps struct {
	client dynamic.Interface
}

// NewApps returns an Apps using client.
func NewApps(client dynamic.Interface) *Apps { return &Apps{client: client} }

// GetApp returns the sync, health and image state of an Application.
func (a *Apps) GetApp(ctx context.Context, namespace, name string) (*streams.AppState, error) {
	obj, err := a.client.Resource(ApplicationResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, appError(namespace, name, err)
	}
	return AppStateOf(obj.Object), nil
}

// RefreshApp sets the refresh annotation of an Application.
func (a *Apps) RefreshApp(ctx context.Context, namespace, name string) error {
	_, err := a.client.Resource(ApplicationResource).Namespace(namespace).Patch(ctx, name, types.MergePatchType, refreshPatch, metav1.PatchOptions{})
	if err != nil {
		return appError(namespace, name, err)
	}
	return nil
}

func appError(namespace, name string, err error) error {
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%s/%s: %w", namespace, name, streams.ErrAppNotFound)
	case apierrors.IsForbidden(err):
		return fmt.Errorf("%s/%s: %w", namespace, name, streams.ErrAppForbidden)
	}
	return fmt.Errorf("application %s/%s: %w", namespace, name, err)
}

// AppStateOf reads the fields argocd steps use from an Application object.
func AppStateOf(obj map[string]any) *streams.AppState {
	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(obj, path...)
		return v
	}
	images, _, _ := unstructured.NestedStringSlice(obj, "status", "summary", "images")
	st := &streams.AppState{
		Sync:             str("status", "sync", "status"),
		Health:           str("status", "health", "status"),
		HealthMessage:    str("status", "health", "message"),
		Revision:         str("status", "sync", "revision"),
		Images:           images,
		OperationPhase:   str("status", "operationState", "phase"),
		OperationMessage: str("status", "operationState", "message"),
	}
	if t, err := time.Parse(time.RFC3339, str("status", "operationState", "finishedAt")); err == nil {
		st.OperationFinishedAt = t
	}
	if st.Revision == "" {
		// Multi-source Applications list one revision per source.
		if revs, _, _ := unstructured.NestedStringSlice(obj, "status", "sync", "revisions"); len(revs) > 0 {
			st.Revision = revs[0]
		}
	}
	return st
}
