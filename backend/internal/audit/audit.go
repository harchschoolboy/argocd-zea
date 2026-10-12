// Package audit records who changed what in Zea: every event is logged as a
// structured line and kept for a number of days in ConfigMaps (one per day,
// capped in size) for the Audit tab.
package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// LabelType marks Zea objects; TypeAudit is its value for audit days.
	LabelType = "argocd-zea.io/type"
	TypeAudit = "audit"
	// LabelDay holds the day (YYYY-MM-DD, UTC) of an audit ConfigMap.
	LabelDay = "argocd-zea.io/day"
	// AnnotationDropped counts events that did not fit into the day.
	AnnotationDropped = "argocd-zea.io/dropped"
	// ConfigMapPrefix is the name prefix of audit ConfigMaps.
	ConfigMapPrefix = "zea-audit-"
	// KeyEvents holds one JSON event per line.
	KeyEvents = "events.jsonl"
	// DayLayout formats days.
	DayLayout = "2006-01-02"

	maxDayBytes   = 900 << 10
	maxDetailLen  = 300
	maxQueue      = 5000
	flushInterval = 3 * time.Second
	pruneInterval = time.Hour
	maxAttempts   = 5
)

// ErrNotFound is returned for days without stored events.
var ErrNotFound = errors.New("no audit events for this day")

// Event is one recorded action.
type Event struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Action string    `json:"action"`
	// Resource and Name identify the item acted on, when known.
	Resource string `json:"resource,omitempty"`
	Name     string `json:"name,omitempty"`
	// Target is a sub-item: a run id, a step.
	Target string `json:"target,omitempty"`
	// Status is the HTTP status of the request.
	Status int `json:"status"`
	// Error is the error message of a failed request.
	Error string `json:"error,omitempty"`
}

// Day is the stored events of one day.
type Day struct {
	Day     string  `json:"day"`
	Events  []Event `json:"events"`
	Dropped int     `json:"dropped,omitempty"`
}

// Store keeps audit events by day.
type Store interface {
	// Append adds events of one day; events that do not fit are counted
	// as dropped.
	Append(ctx context.Context, day string, events []Event) error
	// Days lists the stored days, newest first.
	Days(ctx context.Context) ([]string, error)
	// Get returns the events of a day in the order they were stored.
	Get(ctx context.Context, day string) (*Day, error)
	// Delete removes a day.
	Delete(ctx context.Context, day string) error
}

// Recorder logs events and stores them in batches.
type Recorder struct {
	store     Store
	log       *slog.Logger
	retention int
	now       func() time.Time

	mu      sync.Mutex
	queue   []Event
	dropped int
}

// NewRecorder returns a Recorder keeping events for retention days; a nil
// store or retention 0 only logs them.
func NewRecorder(store Store, retention int, log *slog.Logger) *Recorder {
	if retention <= 0 {
		store = nil
	}
	return &Recorder{store: store, log: log, retention: retention, now: time.Now}
}

// Enabled reports whether events are stored.
func (r *Recorder) Enabled() bool {
	return r != nil && r.store != nil
}

// Retention is the number of days events are kept.
func (r *Recorder) Retention() int {
	if !r.Enabled() {
		return 0
	}
	return r.retention
}

// Record logs e and queues it for storage.
func (r *Recorder) Record(e Event) {
	if r == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = r.now()
	}
	e.Time = e.Time.UTC().Truncate(time.Millisecond)
	if len(e.Error) > maxDetailLen {
		e.Error = e.Error[:maxDetailLen]
	}
	r.log.Info("audit", "user", e.User, "action", e.Action, "resource", e.Resource, "name", e.Name,
		"target", e.Target, "status", e.Status, "error", e.Error)
	if r.store == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) >= maxQueue {
		r.dropped++
		return
	}
	r.queue = append(r.queue, e)
}

// Run flushes queued events and prunes old days until ctx ends, then
// flushes once more.
func (r *Recorder) Run(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	r.Prune(ctx)
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.Flush(final)
			cancel()
			return
		case <-flush.C:
			r.Flush(ctx)
		case <-prune.C:
			r.Prune(ctx)
		}
	}
}

// Flush stores the queued events. Events that cannot be stored are only in
// the log.
func (r *Recorder) Flush(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	queue, dropped := r.queue, r.dropped
	r.queue, r.dropped = nil, 0
	r.mu.Unlock()
	if dropped > 0 {
		r.log.Warn("audit queue full; events were only logged", "dropped", dropped)
	}
	byDay := map[string][]Event{}
	days := []string{}
	for _, e := range queue {
		d := e.Time.Format(DayLayout)
		if _, ok := byDay[d]; !ok {
			days = append(days, d)
		}
		byDay[d] = append(byDay[d], e)
	}
	for _, d := range days {
		if err := r.store.Append(ctx, d, byDay[d]); err != nil {
			r.log.Error("storing audit events failed; they are only logged", "day", d, "events", len(byDay[d]), "error", err)
		}
	}
}

// Prune deletes days older than the retention.
func (r *Recorder) Prune(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	days, err := r.store.Days(ctx)
	if err != nil {
		r.log.Warn("listing audit days failed", "error", err)
		return
	}
	oldest := r.now().UTC().AddDate(0, 0, -(r.retention - 1)).Format(DayLayout)
	for _, d := range days {
		if d < oldest {
			if err := r.store.Delete(ctx, d); err != nil {
				r.log.Warn("deleting audit day failed", "day", d, "error", err)
			}
		}
	}
}

// Days lists the days with events, newest first, including days of queued
// events.
func (r *Recorder) Days(ctx context.Context) ([]string, error) {
	if !r.Enabled() {
		return []string{}, nil
	}
	days, err := r.store.Days(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, d := range days {
		seen[d] = true
	}
	r.mu.Lock()
	for _, e := range r.queue {
		if d := e.Time.Format(DayLayout); !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	r.mu.Unlock()
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

// Get returns the events of a day, newest first, including queued ones.
func (r *Recorder) Get(ctx context.Context, day string) (*Day, error) {
	out := &Day{Day: day, Events: []Event{}}
	if !r.Enabled() {
		return out, nil
	}
	stored, err := r.store.Get(ctx, day)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if stored != nil {
		out = stored
	}
	r.mu.Lock()
	for _, e := range r.queue {
		if e.Time.Format(DayLayout) == day {
			out.Events = append(out.Events, e)
		}
	}
	r.mu.Unlock()
	slices.Reverse(out.Events)
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Time.After(out.Events[j].Time) })
	return out, nil
}

// ValidDay reports whether s is a day in DayLayout.
func ValidDay(s string) bool {
	_, err := time.Parse(DayLayout, s)
	return err == nil && len(s) == len(DayLayout)
}

// ConfigMapStore keeps every day in a labelled ConfigMap.
type ConfigMapStore struct {
	client    kubernetes.Interface
	namespace string
}

// NewConfigMapStore returns a Store backed by ConfigMaps in namespace.
func NewConfigMapStore(client kubernetes.Interface, namespace string) *ConfigMapStore {
	return &ConfigMapStore{client: client, namespace: namespace}
}

// ConfigMapName is the name of the ConfigMap of a day.
func ConfigMapName(day string) string {
	return ConfigMapPrefix + strings.ReplaceAll(day, "-", "")
}

// Append implements Store.
func (s *ConfigMapStore) Append(ctx context.Context, day string, events []Event) error {
	cms := s.client.CoreV1().ConfigMaps(s.namespace)
	for attempt := 1; ; attempt++ {
		cm, err := cms.Get(ctx, ConfigMapName(day), metav1.GetOptions{})
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if create {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapName(day),
				Namespace: s.namespace,
				Labels:    map[string]string{LabelType: TypeAudit, LabelDay: day},
			}}
		}
		if cm.Labels[LabelType] != TypeAudit {
			return fmt.Errorf("configmap %s is not an audit day", cm.Name)
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		if cm.Annotations == nil {
			cm.Annotations = map[string]string{}
		}
		var buf strings.Builder
		buf.WriteString(cm.Data[KeyEvents])
		dropped, _ := strconv.Atoi(cm.Annotations[AnnotationDropped])
		for _, e := range events {
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if buf.Len()+len(line)+1 > maxDayBytes {
				dropped++
				continue
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		cm.Data[KeyEvents] = buf.String()
		if dropped > 0 {
			cm.Annotations[AnnotationDropped] = strconv.Itoa(dropped)
		}
		if create {
			_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		} else {
			_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		}
		if (apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)) && attempt < maxAttempts {
			continue
		}
		return err
	}
}

// Days implements Store.
func (s *ConfigMapStore) Days(ctx context.Context) ([]string, error) {
	list, err := s.client.CoreV1().ConfigMaps(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: LabelType + "=" + TypeAudit})
	if err != nil {
		return nil, err
	}
	days := []string{}
	for _, cm := range list.Items {
		if d := cm.Labels[LabelDay]; ValidDay(d) && cm.Name == ConfigMapName(d) {
			days = append(days, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

// Get implements Store.
func (s *ConfigMapStore) Get(ctx context.Context, day string) (*Day, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, ConfigMapName(day), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cm.Labels[LabelType] != TypeAudit {
		return nil, ErrNotFound
	}
	d := &Day{Day: day, Events: []Event{}}
	d.Dropped, _ = strconv.Atoi(cm.Annotations[AnnotationDropped])
	sc := bufio.NewScanner(strings.NewReader(cm.Data[KeyEvents]))
	sc.Buffer(make([]byte, 0, 64<<10), maxDayBytes)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			d.Events = append(d.Events, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read audit day %s: %w", day, err)
	}
	return d, nil
}

// Delete implements Store.
func (s *ConfigMapStore) Delete(ctx context.Context, day string) error {
	err := s.client.CoreV1().ConfigMaps(s.namespace).Delete(ctx, ConfigMapName(day), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// MemoryStore keeps events in memory (tests).
type MemoryStore struct {
	mu   sync.Mutex
	days map[string]*Day
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{days: map[string]*Day{}}
}

// Append implements Store.
func (s *MemoryStore) Append(_ context.Context, day string, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.days[day]
	if d == nil {
		d = &Day{Day: day}
		s.days[day] = d
	}
	d.Events = append(d.Events, events...)
	return nil
}

// Days implements Store.
func (s *MemoryStore) Days(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for d := range s.days {
		out = append(out, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, day string) (*Day, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.days[day]
	if d == nil {
		return nil, ErrNotFound
	}
	return &Day{Day: d.Day, Events: append([]Event{}, d.Events...), Dropped: d.Dropped}, nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(_ context.Context, day string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.days, day)
	return nil
}
