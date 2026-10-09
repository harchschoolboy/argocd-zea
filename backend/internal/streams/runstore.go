package streams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

const (
	// TypeStreamRun is the value of LabelType for Stream runs.
	TypeStreamRun = "stream-run"
	// LabelStream holds the Stream name of a run.
	LabelStream = "argocd-zea.io/stream"
	// LabelRunPhase is PhaseActive until the run finishes.
	LabelRunPhase = "argocd-zea.io/phase"
	PhaseActive   = "active"
	PhaseFinished = "finished"
	// RunConfigMapNamePrefix is used for run ConfigMaps.
	RunConfigMapNamePrefix = "zea-srun-"
	// KeyRun holds the run as JSON.
	KeyRun = "run.json"

	maxRunBytes       = 900 << 10
	maxMutateAttempts = 5
)

// RunStore persists Stream runs.
type RunStore interface {
	Create(ctx context.Context, r *Run) (*Run, error)
	Get(ctx context.Context, stream, id string) (*Run, error)
	// List returns the runs of a Stream, newest first.
	List(ctx context.Context, stream string) ([]*Run, error)
	// ListActive returns every run that has not finished.
	ListActive(ctx context.Context) ([]*Run, error)
	// Update replaces a run. A non-empty ResourceVersion must match the
	// stored one, otherwise ErrRunConflict is returned.
	Update(ctx context.Context, r *Run) (*Run, error)
	Delete(ctx context.Context, stream, id string) error
}

// RunConfigMapName is the name of a run's ConfigMap.
func RunConfigMapName(stream, id string) string {
	return RunConfigMapNamePrefix + stream + "-" + id
}

// Mutate reads a run, applies fn and writes it back, retrying when another
// writer changed the run in between. fn may run several times.
func Mutate(ctx context.Context, s RunStore, stream, id string, fn func(*Run) error) (*Run, error) {
	for attempt := 1; ; attempt++ {
		r, err := s.Get(ctx, stream, id)
		if err != nil {
			return nil, err
		}
		if err := fn(r); err != nil {
			return nil, err
		}
		updated, err := s.Update(ctx, r)
		if errors.Is(err, ErrRunConflict) && attempt < maxMutateAttempts {
			continue
		}
		return updated, err
	}
}

// PruneRuns deletes the oldest finished runs of a Stream beyond keep.
func PruneRuns(ctx context.Context, s RunStore, stream string, keep int) (int, error) {
	runs, err := s.List(ctx, stream)
	if err != nil {
		return 0, err
	}
	kept, deleted := 0, 0
	for _, r := range runs {
		if r.Status == RunRunning {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := s.Delete(ctx, stream, r.ID); err != nil && !errors.Is(err, ErrRunNotFound) {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func sortRuns(runs []*Run) {
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.After(runs[j].CreatedAt)
		}
		return runs[i].ID > runs[j].ID
	})
}

func runPhase(r *Run) string {
	if r.Status == RunRunning {
		return PhaseActive
	}
	return PhaseFinished
}

// ConfigMapRunStore keeps runs in labelled ConfigMaps of one namespace.
type ConfigMapRunStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewConfigMapRunStore returns a RunStore backed by ConfigMaps.
func NewConfigMapRunStore(client kubernetes.Interface, namespace string) *ConfigMapRunStore {
	return &ConfigMapRunStore{client: client, namespace: namespace}
}

func (s *ConfigMapRunStore) toConfigMap(r *Run) (*corev1.ConfigMap, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encode stream run: %w", err)
	}
	if len(raw) > maxRunBytes {
		return nil, fmt.Errorf("stream run %s is too large to store", r.ID)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            RunConfigMapName(r.Stream, r.ID),
			Namespace:       s.namespace,
			ResourceVersion: r.ResourceVersion,
			Labels: map[string]string{
				LabelType:                  TypeStreamRun,
				LabelStream:                r.Stream,
				LabelRunPhase:              runPhase(r),
				connections.LabelManagedBy: connections.ManagedByZea,
			},
		},
		Data: map[string]string{KeyRun: string(raw)},
	}, nil
}

func runFromConfigMap(cm *corev1.ConfigMap) (*Run, error) {
	var r Run
	if err := json.Unmarshal([]byte(cm.Data[KeyRun]), &r); err != nil {
		return nil, fmt.Errorf("decode stream run %s: %w", cm.Name, err)
	}
	r.ResourceVersion = cm.ResourceVersion
	return &r, nil
}

func (s *ConfigMapRunStore) list(ctx context.Context, selector string) ([]*Run, error) {
	list, err := s.client.CoreV1().ConfigMaps(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list stream run configmaps: %w", err)
	}
	out := make([]*Run, 0, len(list.Items))
	for i := range list.Items {
		r, err := runFromConfigMap(&list.Items[i])
		if err != nil || !ValidRunID(r.ID) || r.Stream == "" {
			continue
		}
		out = append(out, r)
	}
	sortRuns(out)
	return out, nil
}

func (s *ConfigMapRunStore) Create(ctx context.Context, r *Run) (*Run, error) {
	cp := cloneRun(r)
	cp.ResourceVersion = ""
	cm, err := s.toConfigMap(cp)
	if err != nil {
		return nil, err
	}
	created, err := s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil, ErrRunConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create stream run configmap: %w", err)
	}
	return runFromConfigMap(created)
}

func (s *ConfigMapRunStore) Get(ctx context.Context, stream, id string) (*Run, error) {
	if !ValidRunID(id) || ValidateName(stream) != nil {
		return nil, ErrRunNotFound
	}
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, RunConfigMapName(stream, id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get stream run configmap: %w", err)
	}
	if cm.Labels[LabelType] != TypeStreamRun {
		return nil, ErrRunNotFound
	}
	r, err := runFromConfigMap(cm)
	if err != nil {
		return nil, err
	}
	if r.Stream != stream || r.ID != id {
		return nil, ErrRunNotFound
	}
	return r, nil
}

func (s *ConfigMapRunStore) List(ctx context.Context, stream string) ([]*Run, error) {
	if ValidateName(stream) != nil {
		return []*Run{}, nil
	}
	return s.list(ctx, fmt.Sprintf("%s=%s,%s=%s", LabelType, TypeStreamRun, LabelStream, stream))
}

func (s *ConfigMapRunStore) ListActive(ctx context.Context) ([]*Run, error) {
	return s.list(ctx, fmt.Sprintf("%s=%s,%s=%s", LabelType, TypeStreamRun, LabelRunPhase, PhaseActive))
}

func (s *ConfigMapRunStore) Update(ctx context.Context, r *Run) (*Run, error) {
	cm, err := s.toConfigMap(r)
	if err != nil {
		return nil, err
	}
	updated, err := s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	switch {
	case apierrors.IsConflict(err):
		return nil, ErrRunConflict
	case apierrors.IsNotFound(err):
		return nil, ErrRunNotFound
	case err != nil:
		return nil, fmt.Errorf("update stream run configmap: %w", err)
	}
	return runFromConfigMap(updated)
}

func (s *ConfigMapRunStore) Delete(ctx context.Context, stream, id string) error {
	if !ValidRunID(id) || ValidateName(stream) != nil {
		return ErrRunNotFound
	}
	err := s.client.CoreV1().ConfigMaps(s.namespace).Delete(ctx, RunConfigMapName(stream, id), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return ErrRunNotFound
	}
	return err
}

// MemoryRunStore is an in-memory RunStore for tests and local development.
type MemoryRunStore struct {
	mu      sync.Mutex
	items   map[string]*Run
	version int
}

// NewMemoryRunStore returns an empty MemoryRunStore.
func NewMemoryRunStore() *MemoryRunStore {
	return &MemoryRunStore{items: map[string]*Run{}}
}

func (m *MemoryRunStore) nextVersion() string {
	m.version++
	return strconv.Itoa(m.version)
}

func (m *MemoryRunStore) Create(_ context.Context, r *Run) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := RunConfigMapName(r.Stream, r.ID)
	if _, ok := m.items[key]; ok {
		return nil, ErrRunConflict
	}
	cp := cloneRun(r)
	cp.ResourceVersion = m.nextVersion()
	m.items[key] = cp
	return cloneRun(cp), nil
}

func (m *MemoryRunStore) Get(_ context.Context, stream, id string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.items[RunConfigMapName(stream, id)]
	if !ok {
		return nil, ErrRunNotFound
	}
	return cloneRun(r), nil
}

func (m *MemoryRunStore) filter(keep func(*Run) bool) []*Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Run{}
	for _, r := range m.items {
		if keep(r) {
			out = append(out, cloneRun(r))
		}
	}
	sortRuns(out)
	return out
}

func (m *MemoryRunStore) List(_ context.Context, stream string) ([]*Run, error) {
	return m.filter(func(r *Run) bool { return r.Stream == stream }), nil
}

func (m *MemoryRunStore) ListActive(context.Context) ([]*Run, error) {
	return m.filter(func(r *Run) bool { return r.Status == RunRunning }), nil
}

func (m *MemoryRunStore) Update(_ context.Context, r *Run) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := RunConfigMapName(r.Stream, r.ID)
	cur, ok := m.items[key]
	if !ok {
		return nil, ErrRunNotFound
	}
	if r.ResourceVersion != "" && r.ResourceVersion != cur.ResourceVersion {
		return nil, ErrRunConflict
	}
	cp := cloneRun(r)
	cp.ResourceVersion = m.nextVersion()
	m.items[key] = cp
	return cloneRun(cp), nil
}

func (m *MemoryRunStore) Delete(_ context.Context, stream, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := RunConfigMapName(stream, id)
	if _, ok := m.items[key]; !ok {
		return ErrRunNotFound
	}
	delete(m.items, key)
	return nil
}

// cloneRun deep-copies a run through JSON.
func cloneRun(r *Run) *Run {
	raw, err := json.Marshal(r)
	if err != nil {
		panic(fmt.Sprintf("clone stream run: %v", err))
	}
	var cp Run
	if err := json.Unmarshal(raw, &cp); err != nil {
		panic(fmt.Sprintf("clone stream run: %v", err))
	}
	cp.ResourceVersion = r.ResourceVersion
	return &cp
}
