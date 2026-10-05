package registries

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

// Store persists registries.
type Store interface {
	List(ctx context.Context) ([]*Registry, error)
	Get(ctx context.Context, name string) (*Registry, error)
	Create(ctx context.Context, r *Registry) (*Registry, error)
	// Update replaces public fields. Credential keys with empty values keep
	// their stored value; "-" removes a key.
	Update(ctx context.Context, r *Registry) (*Registry, error)
	Delete(ctx context.Context, name string) error
}

// SecretStore keeps registries in labelled Secrets of one namespace (the
// Connections namespace).
type SecretStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewSecretStore returns a Store backed by Kubernetes Secrets.
func NewSecretStore(client kubernetes.Interface, namespace string) *SecretStore {
	return &SecretStore{client: client, namespace: namespace}
}

// List returns all registries sorted by name.
func (s *SecretStore) List(ctx context.Context) ([]*Registry, error) {
	opts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", connections.LabelSecretType, SecretTypeRegistry)}
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list registry secrets: %w", err)
	}
	out := make([]*Registry, 0, len(list.Items))
	seen := map[string]bool{}
	for i := range list.Items {
		r := FromSecret(&list.Items[i])
		if r.Name == "" || seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get finds a registry by its logical name.
func (s *SecretStore) Get(ctx context.Context, name string) (*Registry, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

// Create stores a new UI-managed registry.
func (s *SecretStore) Create(ctx context.Context, r *Registry) (*Registry, error) {
	if _, err := s.Get(ctx, r.Name); err == nil {
		return nil, ErrAlreadyExists
	} else if err != ErrNotFound {
		return nil, err
	}
	sec := ToSecret(r)
	sec.Name = SecretNamePrefix + r.Name
	sec.Namespace = s.namespace
	created, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, sec, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil, ErrAlreadyExists
	}
	if err != nil {
		return nil, fmt.Errorf("create registry secret: %w", err)
	}
	return FromSecret(created), nil
}

// Update modifies a UI-managed registry.
func (s *SecretStore) Update(ctx context.Context, r *Registry) (*Registry, error) {
	cur, err := s.Get(ctx, r.Name)
	if err != nil {
		return nil, err
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	sec, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, cur.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get registry secret: %w", err)
	}
	next := *r
	next.Credentials = connections.MergeCredentials(cur.Credentials, r.Credentials)
	desired := ToSecret(&next)
	labels := map[string]string{}
	for k, v := range sec.Labels {
		labels[k] = v
	}
	for k, v := range desired.Labels {
		labels[k] = v
	}
	sec.Labels = labels
	sec.Data = nil
	sec.StringData = desired.StringData
	updated, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update registry secret: %w", err)
	}
	return FromSecret(updated), nil
}

// Delete removes a UI-managed registry.
func (s *SecretStore) Delete(ctx context.Context, name string) error {
	cur, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if !cur.Editable {
		return ErrReadOnly
	}
	err = s.client.CoreV1().Secrets(s.namespace).Delete(ctx, cur.SecretName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// FromSecret maps a Secret to a registry. Declarative Secrets may be of type
// kubernetes.io/dockerconfigjson; their .dockerconfigjson key is used as is.
func FromSecret(sec *corev1.Secret) *Registry {
	vals := map[string]string{}
	for k, v := range sec.Data {
		vals[k] = string(v)
	}
	for k, v := range sec.StringData {
		vals[k] = v
	}
	r := &Registry{
		Name:        strings.TrimSpace(vals[KeyName]),
		Kind:        strings.TrimSpace(vals[KeyKind]),
		URL:         strings.TrimSpace(vals[KeyURL]),
		Credentials: map[string]string{},
		Editable:    sec.Labels[connections.LabelManagedBy] == connections.ManagedByZea,
		SecretName:  sec.Name,
	}
	for k, v := range vals {
		if !isReservedKey(k) {
			r.Credentials[k] = v
		}
	}
	return r
}

// ToSecret maps a registry to a UI-managed Secret (without name/namespace).
func ToSecret(r *Registry) *corev1.Secret {
	data := map[string]string{
		KeyName: r.Name,
		KeyKind: r.Kind,
		KeyURL:  r.URL,
	}
	for k, v := range r.Credentials {
		if v != "" {
			data[k] = v
		}
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				connections.LabelSecretType: SecretTypeRegistry,
				connections.LabelManagedBy:  connections.ManagedByZea,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}
}

// MemoryStore is an in-memory Store for tests and local development.
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]*Registry
}

// NewMemoryStore returns a MemoryStore seeded with regs.
func NewMemoryStore(regs ...*Registry) *MemoryStore {
	m := &MemoryStore{items: map[string]*Registry{}}
	for _, r := range regs {
		cp := *r
		m.items[r.Name] = &cp
	}
	return m
}

func (m *MemoryStore) List(context.Context) ([]*Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Registry, 0, len(m.items))
	for _, r := range m.items {
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, name string) (*Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.items[name]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (m *MemoryStore) Create(_ context.Context, r *Registry) (*Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[r.Name]; ok {
		return nil, ErrAlreadyExists
	}
	cp := *r
	cp.Editable = true
	cp.SecretName = SecretNamePrefix + r.Name
	m.items[r.Name] = &cp
	out := cp
	return &out, nil
}

func (m *MemoryStore) Update(_ context.Context, r *Registry) (*Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.items[r.Name]
	if !ok {
		return nil, ErrNotFound
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	cp := *r
	cp.Credentials = connections.MergeCredentials(cur.Credentials, r.Credentials)
	cp.Editable = true
	cp.SecretName = cur.SecretName
	m.items[r.Name] = &cp
	out := cp
	return &out, nil
}

func (m *MemoryStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.items[name]
	if !ok {
		return ErrNotFound
	}
	if !cur.Editable {
		return ErrReadOnly
	}
	delete(m.items, name)
	return nil
}
