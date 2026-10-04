package connections

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
)

// Store persists Connections.
type Store interface {
	List(ctx context.Context) ([]*Connection, error)
	Get(ctx context.Context, name string) (*Connection, error)
	Create(ctx context.Context, c *Connection) (*Connection, error)
	// Update replaces public fields. Credential keys with empty values keep
	// their stored value, so clients never need to resend secrets.
	Update(ctx context.Context, c *Connection) (*Connection, error)
	Delete(ctx context.Context, name string) error
}

// SecretStore keeps Connections in labelled Secrets of one namespace.
type SecretStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewSecretStore returns a Store backed by Kubernetes Secrets.
func NewSecretStore(client kubernetes.Interface, namespace string) *SecretStore {
	return &SecretStore{client: client, namespace: namespace}
}

// List returns all Connections sorted by name.
func (s *SecretStore) List(ctx context.Context) ([]*Connection, error) {
	opts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", LabelSecretType, SecretTypeConnection)}
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list connection secrets: %w", err)
	}
	out := make([]*Connection, 0, len(list.Items))
	seen := map[string]bool{}
	for i := range list.Items {
		c := FromSecret(&list.Items[i])
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get finds a Connection by its logical name. Declarative Secrets may have any
// metadata name, so lookup goes through the label-filtered list.
func (s *SecretStore) Get(ctx context.Context, name string) (*Connection, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.Name == name {
			return c, nil
		}
	}
	return nil, ErrNotFound
}

// Create stores a new UI-managed Connection.
func (s *SecretStore) Create(ctx context.Context, c *Connection) (*Connection, error) {
	if _, err := s.Get(ctx, c.Name); err == nil {
		return nil, ErrAlreadyExists
	} else if err != ErrNotFound {
		return nil, err
	}
	sec := ToSecret(c)
	sec.Name = SecretNamePrefix + c.Name
	sec.Namespace = s.namespace
	created, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, sec, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil, ErrAlreadyExists
	}
	if err != nil {
		return nil, fmt.Errorf("create connection secret: %w", err)
	}
	return FromSecret(created), nil
}

// Update modifies a UI-managed Connection.
func (s *SecretStore) Update(ctx context.Context, c *Connection) (*Connection, error) {
	cur, err := s.Get(ctx, c.Name)
	if err != nil {
		return nil, err
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	sec, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, cur.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get connection secret: %w", err)
	}
	merged := MergeCredentials(cur.Credentials, c.Credentials)
	next := *c
	next.Credentials = merged
	desired := ToSecret(&next)
	sec.Labels = mergeMaps(sec.Labels, desired.Labels)
	sec.Data = nil
	sec.StringData = desired.StringData
	updated, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update connection secret: %w", err)
	}
	return FromSecret(updated), nil
}

// Delete removes a UI-managed Connection.
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

// FromSecret maps a Secret to a Connection. Both Data and StringData are
// read so objects returned by fake clients (StringData only) work too.
func FromSecret(sec *corev1.Secret) *Connection {
	vals := map[string]string{}
	for k, v := range sec.Data {
		vals[k] = string(v)
	}
	for k, v := range sec.StringData {
		vals[k] = v
	}
	c := &Connection{
		Name:          strings.TrimSpace(vals[KeyName]),
		Provider:      strings.TrimSpace(vals[KeyProvider]),
		URL:           strings.TrimSpace(vals[KeyURL]),
		APIURL:        strings.TrimSpace(vals[KeyAPIURL]),
		AllowedGroups: splitList(vals[KeyAllowedGroups]),
		Credentials:   map[string]string{},
		Editable:      sec.Labels[LabelManagedBy] == ManagedByZea,
		SecretName:    sec.Name,
	}
	for k, v := range vals {
		if !isReservedKey(k) {
			c.Credentials[k] = v
		}
	}
	return c
}

// ToSecret maps a Connection to a UI-managed Secret (without name/namespace).
func ToSecret(c *Connection) *corev1.Secret {
	data := map[string]string{
		KeyName:          c.Name,
		KeyProvider:      c.Provider,
		KeyURL:           c.URL,
		KeyAllowedGroups: strings.Join(c.AllowedGroups, ","),
	}
	if c.APIURL != "" {
		data[KeyAPIURL] = c.APIURL
	}
	for k, v := range c.Credentials {
		if v != "" {
			data[k] = v
		}
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				LabelSecretType: SecretTypeConnection,
				LabelManagedBy:  ManagedByZea,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}
}

// MergeCredentials keeps stored values for keys sent empty or omitted.
// A key is removed only when explicitly sent with the value "-".
func MergeCredentials(stored, incoming map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		switch v {
		case "":
		case "-":
			delete(out, k)
		default:
			out[k] = v
		}
	}
	return out
}

func mergeMaps(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func splitList(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// MemoryStore is an in-memory Store for tests and local development.
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]*Connection
}

// NewMemoryStore returns an empty MemoryStore seeded with conns.
func NewMemoryStore(conns ...*Connection) *MemoryStore {
	m := &MemoryStore{items: map[string]*Connection{}}
	for _, c := range conns {
		cp := *c
		m.items[c.Name] = &cp
	}
	return m
}

func (m *MemoryStore) List(context.Context) ([]*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Connection, 0, len(m.items))
	for _, c := range m.items {
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, name string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[name]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *MemoryStore) Create(_ context.Context, c *Connection) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[c.Name]; ok {
		return nil, ErrAlreadyExists
	}
	cp := *c
	cp.Editable = true
	cp.SecretName = SecretNamePrefix + c.Name
	m.items[c.Name] = &cp
	out := cp
	return &out, nil
}

func (m *MemoryStore) Update(_ context.Context, c *Connection) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.items[c.Name]
	if !ok {
		return nil, ErrNotFound
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	cp := *c
	cp.Credentials = MergeCredentials(cur.Credentials, c.Credentials)
	cp.Editable = true
	cp.SecretName = cur.SecretName
	m.items[c.Name] = &cp
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
