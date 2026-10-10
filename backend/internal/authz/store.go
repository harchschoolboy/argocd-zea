package authz

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

const (
	// ConfigMapName is the ConfigMap holding the policy.
	ConfigMapName = "zea-policy"
	// LabelType marks Zea ConfigMaps; TypePolicy is its value for the policy.
	LabelType  = "argocd-zea.io/type"
	TypePolicy = "policy"
	// KeyPolicy holds the policy YAML.
	KeyPolicy = "policy.yaml"

	annotationArgoTracking = "argocd.argoproj.io/tracking-id"
	labelArgoInstance      = "app.kubernetes.io/instance"
)

var (
	// ErrReadOnly is returned when the policy is managed outside Zea.
	ErrReadOnly = errors.New("the access policy is managed outside Zea (for example by Argo CD) and cannot be edited here")
	// ErrConflict is returned when the policy changed since it was read.
	ErrConflict = errors.New("the access policy was changed by someone else; reload it and try again")
)

// Document is the stored policy with its metadata.
type Document struct {
	Policy Policy
	// Exists is false until the policy is saved for the first time.
	Exists bool
	// Editable is false when the ConfigMap is managed outside Zea.
	Editable bool
	// Version must be sent back on save to detect concurrent edits.
	Version string
	// Error explains why a stored policy cannot be used. Such a policy
	// grants nothing, so only admins have access until it is fixed.
	Error string
}

// PolicyStore persists the policy.
type PolicyStore interface {
	Load(ctx context.Context) (*Document, error)
	// Save creates or replaces the policy. Version must match the stored
	// one ("" when the policy does not exist yet).
	Save(ctx context.Context, p Policy, version string) (*Document, error)
}

// documentOf parses raw YAML and rejects invalid policies as a whole.
func documentOf(raw string) *Document {
	d := &Document{Exists: true, Policy: Policy{Roles: []Role{}, Bindings: []Binding{}}}
	p, err := ParsePolicy(raw)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	if problems := p.Problems(); len(problems) > 0 {
		d.Policy = p
		d.Error = "invalid policy: " + problems[0].String()
		if len(problems) > 1 {
			d.Error += fmt.Sprintf(" (and %d more)", len(problems)-1)
		}
		return d
	}
	d.Policy = p
	return d
}

// ConfigMapPolicyStore keeps the policy in one ConfigMap.
type ConfigMapPolicyStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewConfigMapPolicyStore returns a PolicyStore backed by a ConfigMap.
func NewConfigMapPolicyStore(client kubernetes.Interface, namespace string) *ConfigMapPolicyStore {
	return &ConfigMapPolicyStore{client: client, namespace: namespace}
}

func (s *ConfigMapPolicyStore) Load(ctx context.Context) (*Document, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &Document{Editable: true, Policy: Policy{Roles: []Role{}, Bindings: []Binding{}}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy configmap: %w", err)
	}
	d := documentOf(cm.Data[KeyPolicy])
	d.Version = cm.ResourceVersion
	d.Editable = editable(cm)
	return d, nil
}

func (s *ConfigMapPolicyStore) Save(ctx context.Context, p Policy, version string) (*Document, error) {
	raw, err := FormatPolicy(p)
	if err != nil {
		return nil, fmt.Errorf("encode policy: %w", err)
	}
	cms := s.client.CoreV1().ConfigMaps(s.namespace)
	cm, err := cms.Get(ctx, ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if version != "" {
			return nil, ErrConflict
		}
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapName,
				Namespace: s.namespace,
				Labels:    map[string]string{LabelType: TypePolicy, connections.LabelManagedBy: connections.ManagedByZea},
			},
			Data: map[string]string{KeyPolicy: raw},
		}
		created, err := cms.Create(ctx, cm, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil, ErrConflict
		}
		if err != nil {
			return nil, fmt.Errorf("create policy configmap: %w", err)
		}
		return s.docOf(created), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy configmap: %w", err)
	}
	if !editable(cm) {
		return nil, ErrReadOnly
	}
	if version != cm.ResourceVersion {
		return nil, ErrConflict
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[KeyPolicy] = raw
	updated, err := cms.Update(ctx, cm, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("update policy configmap: %w", err)
	}
	return s.docOf(updated), nil
}

func (s *ConfigMapPolicyStore) docOf(cm *corev1.ConfigMap) *Document {
	d := documentOf(cm.Data[KeyPolicy])
	d.Version = cm.ResourceVersion
	d.Editable = editable(cm)
	return d
}

// editable: only ConfigMaps created by Zea and not adopted by Argo CD.
func editable(cm *corev1.ConfigMap) bool {
	return cm.Labels[connections.LabelManagedBy] == connections.ManagedByZea &&
		cm.Annotations[annotationArgoTracking] == "" && cm.Labels[labelArgoInstance] == ""
}

// MemoryPolicyStore is an in-memory PolicyStore for tests and local runs.
type MemoryPolicyStore struct {
	mu       sync.Mutex
	raw      string
	exists   bool
	readOnly bool
	version  int
}

// NewMemoryPolicyStore returns an empty MemoryPolicyStore.
func NewMemoryPolicyStore() *MemoryPolicyStore { return &MemoryPolicyStore{} }

// SetRaw stores YAML as is, bypassing validation, like an edit outside Zea.
func (m *MemoryPolicyStore) SetRaw(raw string, readOnly bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw, m.exists, m.readOnly = raw, true, readOnly
	m.version++
}

func (m *MemoryPolicyStore) Load(context.Context) (*Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doc(), nil
}

func (m *MemoryPolicyStore) doc() *Document {
	if !m.exists {
		return &Document{Editable: true, Policy: Policy{Roles: []Role{}, Bindings: []Binding{}}}
	}
	d := documentOf(m.raw)
	d.Version = strconv.Itoa(m.version)
	d.Editable = !m.readOnly
	return d
}

func (m *MemoryPolicyStore) Save(_ context.Context, p Policy, version string) (*Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exists && m.readOnly {
		return nil, ErrReadOnly
	}
	cur := ""
	if m.exists {
		cur = strconv.Itoa(m.version)
	}
	if strings.TrimSpace(version) != cur {
		return nil, ErrConflict
	}
	raw, err := FormatPolicy(p)
	if err != nil {
		return nil, err
	}
	m.raw, m.exists = raw, true
	m.version++
	return m.doc(), nil
}
