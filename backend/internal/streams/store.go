package streams

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

const (
	// LabelType marks a ConfigMap as a Zea object.
	LabelType = "argocd-zea.io/type"
	// TypeStream is the value of LabelType for Streams.
	TypeStream = "stream"
	// ConfigMapNamePrefix is used for Stream ConfigMaps.
	ConfigMapNamePrefix = "zea-stream-"

	KeyName    = "name"
	KeyDraftOf = "draftOf"
	KeySpec    = "stream.yaml"

	// SettingsConfigMapName holds the settings of the Streams page. Its name
	// cannot clash with ConfigMapNamePrefix.
	SettingsConfigMapName = "zea-streams-settings"
	// TypeStreamSettings is the value of LabelType for the settings.
	TypeStreamSettings = "stream-settings"
	// KeyCategoryOrder holds the manual order of categories as a YAML list.
	KeyCategoryOrder = "categoryOrder.yaml"
	// KeyStreamOrder holds the manual order of Streams as a YAML list of
	// names; it orders the Streams inside each category.
	KeyStreamOrder = "streamOrder.yaml"

	// Argo CD marks the resources it manages with one of these.
	annotationArgoTracking = "argocd.argoproj.io/tracking-id"
	labelArgoInstance      = "app.kubernetes.io/instance"
)

// Store persists Streams.
type Store interface {
	List(ctx context.Context) ([]*Stream, error)
	Get(ctx context.Context, name string) (*Stream, error)
	Create(ctx context.Context, s *Stream) (*Stream, error)
	// Update replaces a UI-managed Stream. A non-empty ResourceVersion must
	// match the stored one.
	Update(ctx context.Context, s *Stream) (*Stream, error)
	Delete(ctx context.Context, name string) error
	// CategoryOrder returns the manual order of categories on the Streams
	// page; categories missing from it follow in alphabetical order.
	CategoryOrder(ctx context.Context) ([]string, error)
	// SetCategoryOrder replaces the manual order of categories.
	SetCategoryOrder(ctx context.Context, order []string) error
	// StreamOrder returns the manual order of Streams (names); Streams
	// missing from it follow by label.
	StreamOrder(ctx context.Context) ([]string, error)
	// SetStreamOrder replaces the manual order of Streams.
	SetStreamOrder(ctx context.Context, order []string) error
}

// ErrSettingsReadOnly is returned when the Streams page settings are
// managed outside Zea.
var ErrSettingsReadOnly = errors.New("the streams settings are managed outside Zea (for example by Argo CD) and cannot be edited here")

// ParseSpec reads the YAML stored under KeySpec. Unknown fields are errors.
func ParseSpec(raw string) (Spec, error) {
	var spec Spec
	if strings.TrimSpace(raw) == "" {
		return spec, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("invalid %s: %v", KeySpec, err)
	}
	return spec, nil
}

// FormatSpec renders a Spec for KeySpec.
func FormatSpec(spec Spec) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(spec); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ConfigMapStore keeps Streams in labelled ConfigMaps of one namespace.
type ConfigMapStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewConfigMapStore returns a Store backed by ConfigMaps.
func NewConfigMapStore(client kubernetes.Interface, namespace string) *ConfigMapStore {
	return &ConfigMapStore{client: client, namespace: namespace}
}

// Namespace is where Streams are stored.
func (s *ConfigMapStore) Namespace() string { return s.namespace }

// List returns all Streams sorted by name.
func (s *ConfigMapStore) List(ctx context.Context) ([]*Stream, error) {
	opts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", LabelType, TypeStream)}
	list, err := s.client.CoreV1().ConfigMaps(s.namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list stream configmaps: %w", err)
	}
	out := make([]*Stream, 0, len(list.Items))
	seen := map[string]bool{}
	for i := range list.Items {
		st := FromConfigMap(&list.Items[i])
		if st.Name == "" || seen[st.Name] {
			continue
		}
		seen[st.Name] = true
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get finds a Stream by its logical name.
func (s *ConfigMapStore) Get(ctx context.Context, name string) (*Stream, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range all {
		if st.Name == name {
			return st, nil
		}
	}
	return nil, ErrNotFound
}

// Create stores a new UI-managed Stream.
func (s *ConfigMapStore) Create(ctx context.Context, st *Stream) (*Stream, error) {
	if _, err := s.Get(ctx, st.Name); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	cm, err := ToConfigMap(st)
	if err != nil {
		return nil, err
	}
	cm.Name = ConfigMapNamePrefix + st.Name
	cm.Namespace = s.namespace
	created, err := s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil, ErrAlreadyExists
	}
	if err != nil {
		return nil, fmt.Errorf("create stream configmap: %w", err)
	}
	return FromConfigMap(created), nil
}

// Update replaces a UI-managed Stream.
func (s *ConfigMapStore) Update(ctx context.Context, st *Stream) (*Stream, error) {
	cur, err := s.Get(ctx, st.Name)
	if err != nil {
		return nil, err
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, cur.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get stream configmap: %w", err)
	}
	if st.ResourceVersion != "" && st.ResourceVersion != cm.ResourceVersion {
		return nil, ErrConflict
	}
	desired, err := ToConfigMap(st)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for k, v := range cm.Labels {
		labels[k] = v
	}
	for k, v := range desired.Labels {
		labels[k] = v
	}
	cm.Labels = labels
	cm.Data = desired.Data
	updated, err := s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("update stream configmap: %w", err)
	}
	return FromConfigMap(updated), nil
}

// Delete removes a UI-managed Stream.
func (s *ConfigMapStore) Delete(ctx context.Context, name string) error {
	cur, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if !cur.Editable {
		return ErrReadOnly
	}
	err = s.client.CoreV1().ConfigMaps(s.namespace).Delete(ctx, cur.ConfigMapName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// CategoryOrder reads the order from the settings ConfigMap. A missing or
// unreadable order is empty.
func (s *ConfigMapStore) CategoryOrder(ctx context.Context) ([]string, error) {
	return s.readOrder(ctx, KeyCategoryOrder, NormalizeCategoryOrder)
}

// SetCategoryOrder writes the order to the settings ConfigMap.
func (s *ConfigMapStore) SetCategoryOrder(ctx context.Context, order []string) error {
	return s.writeOrder(ctx, KeyCategoryOrder, order)
}

// StreamOrder reads the order of Streams from the settings ConfigMap. A
// missing or unreadable order is empty.
func (s *ConfigMapStore) StreamOrder(ctx context.Context) ([]string, error) {
	return s.readOrder(ctx, KeyStreamOrder, NormalizeStreamOrder)
}

// SetStreamOrder writes the order of Streams to the settings ConfigMap.
func (s *ConfigMapStore) SetStreamOrder(ctx context.Context, order []string) error {
	return s.writeOrder(ctx, KeyStreamOrder, order)
}

// readOrder reads a YAML list from the settings ConfigMap.
func (s *ConfigMapStore) readOrder(ctx context.Context, key string, normalize func([]string) ([]string, error)) ([]string, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, SettingsConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get streams settings configmap: %w", err)
	}
	var order []string
	if yaml.Unmarshal([]byte(cm.Data[key]), &order) != nil {
		return []string{}, nil
	}
	if order, err = normalize(order); err != nil {
		return []string{}, nil
	}
	return order, nil
}

// writeOrder writes a YAML list to the settings ConfigMap, creating it when
// missing.
func (s *ConfigMapStore) writeOrder(ctx context.Context, key string, order []string) error {
	raw, err := yaml.Marshal(order)
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	cms := s.client.CoreV1().ConfigMaps(s.namespace)
	cm, err := cms.Get(ctx, SettingsConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SettingsConfigMapName,
				Namespace: s.namespace,
				Labels:    map[string]string{LabelType: TypeStreamSettings, connections.LabelManagedBy: connections.ManagedByZea},
			},
			Data: map[string]string{key: string(raw)},
		}
		if _, err := cms.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create streams settings configmap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get streams settings configmap: %w", err)
	}
	if cm.Labels[connections.LabelManagedBy] != connections.ManagedByZea || managedByArgoCD(cm) {
		return ErrSettingsReadOnly
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = string(raw)
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update streams settings configmap: %w", err)
	}
	return nil
}

// FromConfigMap maps a ConfigMap to a Stream. ConfigMaps created outside
// the UI, or adopted by Argo CD, are read-only.
func FromConfigMap(cm *corev1.ConfigMap) *Stream {
	st := &Stream{
		Name:            strings.TrimSpace(cm.Data[KeyName]),
		DraftOf:         strings.TrimSpace(cm.Data[KeyDraftOf]),
		ConfigMapName:   cm.Name,
		ResourceVersion: cm.ResourceVersion,
		Editable:        cm.Labels[connections.LabelManagedBy] == connections.ManagedByZea && !managedByArgoCD(cm),
	}
	if st.Name == "" {
		st.Name = strings.TrimPrefix(cm.Name, ConfigMapNamePrefix)
	}
	spec, err := ParseSpec(cm.Data[KeySpec])
	if err != nil {
		st.ParseError = err.Error()
	}
	st.Spec = spec
	return st
}

func managedByArgoCD(cm *corev1.ConfigMap) bool {
	return cm.Annotations[annotationArgoTracking] != "" || cm.Labels[labelArgoInstance] != ""
}

// ToConfigMap maps a Stream to a UI-managed ConfigMap (without name and
// namespace).
func ToConfigMap(st *Stream) (*corev1.ConfigMap, error) {
	spec, err := FormatSpec(st.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode stream: %w", err)
	}
	data := map[string]string{KeyName: st.Name, KeySpec: spec}
	if st.DraftOf != "" {
		data[KeyDraftOf] = st.DraftOf
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				LabelType:                  TypeStream,
				connections.LabelManagedBy: connections.ManagedByZea,
			},
		},
		Data: data,
	}, nil
}

// MemoryStore is an in-memory Store for tests and local development.
type MemoryStore struct {
	mu            sync.Mutex
	items         map[string]*Stream
	version       int
	categoryOrder []string
	streamOrder   []string
}

// NewMemoryStore returns a MemoryStore seeded with streams.
func NewMemoryStore(streams ...*Stream) *MemoryStore {
	m := &MemoryStore{items: map[string]*Stream{}}
	for _, st := range streams {
		cp := clone(st)
		if cp.ConfigMapName == "" {
			cp.ConfigMapName = ConfigMapNamePrefix + st.Name
		}
		cp.ResourceVersion = m.nextVersion()
		m.items[st.Name] = cp
	}
	return m
}

func (m *MemoryStore) nextVersion() string {
	m.version++
	return strconv.Itoa(m.version)
}

func (m *MemoryStore) List(context.Context) ([]*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Stream, 0, len(m.items))
	for _, st := range m.items {
		out = append(out, clone(st))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, name string) (*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.items[name]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(st), nil
}

func (m *MemoryStore) Create(_ context.Context, st *Stream) (*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[st.Name]; ok {
		return nil, ErrAlreadyExists
	}
	cp := clone(st)
	cp.Editable = true
	cp.ConfigMapName = ConfigMapNamePrefix + st.Name
	cp.ResourceVersion = m.nextVersion()
	m.items[st.Name] = cp
	return clone(cp), nil
}

func (m *MemoryStore) Update(_ context.Context, st *Stream) (*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.items[st.Name]
	if !ok {
		return nil, ErrNotFound
	}
	if !cur.Editable {
		return nil, ErrReadOnly
	}
	if st.ResourceVersion != "" && st.ResourceVersion != cur.ResourceVersion {
		return nil, ErrConflict
	}
	cp := clone(st)
	cp.Editable = true
	cp.ConfigMapName = cur.ConfigMapName
	cp.ResourceVersion = m.nextVersion()
	m.items[st.Name] = cp
	return clone(cp), nil
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

func (m *MemoryStore) CategoryOrder(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.categoryOrder...), nil
}

func (m *MemoryStore) SetCategoryOrder(_ context.Context, order []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.categoryOrder = append([]string{}, order...)
	return nil
}

func (m *MemoryStore) StreamOrder(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.streamOrder...), nil
}

func (m *MemoryStore) SetStreamOrder(_ context.Context, order []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamOrder = append([]string{}, order...)
	return nil
}

// clone deep-copies a Stream.
func clone(st *Stream) *Stream {
	cp := *st
	cp.Params = append([]Param(nil), st.Params...)
	for i := range cp.Params {
		cp.Params[i].Options = append([]string(nil), st.Params[i].Options...)
		cp.Params[i].OptionLabels = copyMap(st.Params[i].OptionLabels)
	}
	cp.Stages = make([]Stage, len(st.Stages))
	for i, stage := range st.Stages {
		cp.Stages[i] = Stage{Name: stage.Name, Steps: make([]Step, len(stage.Steps))}
		for j, step := range stage.Steps {
			c := step
			c.Inputs = copyMap(step.Inputs)
			c.Variables = copyMap(step.Variables)
			c.Needs = append([]string(nil), step.Needs...)
			cp.Stages[i].Steps[j] = c
		}
	}
	return &cp
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
