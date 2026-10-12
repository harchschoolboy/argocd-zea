package audit

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecorderFlushGetAndPrune(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	r := NewRecorder(store, 2, quiet)
	r.now = func() time.Time { return at("2025-03-10T12:00:00Z") }
	r.Record(Event{Time: at("2025-03-08T10:00:00Z"), User: "a", Action: "old"})
	r.Record(Event{Time: at("2025-03-09T10:00:00Z"), User: "a", Action: "kept"})
	r.Record(Event{Time: at("2025-03-10T10:00:00Z"), User: "a", Action: "first"})
	r.Flush(ctx)
	r.Record(Event{User: "b", Action: "queued", Error: strings.Repeat("x", 400)})

	days, _ := r.Days(ctx)
	if strings.Join(days, ",") != "2025-03-10,2025-03-09,2025-03-08" {
		t.Fatalf("days = %v", days)
	}
	d, err := r.Get(ctx, "2025-03-10")
	if err != nil || len(d.Events) != 2 || d.Events[0].Action != "queued" || d.Events[1].Action != "first" {
		t.Fatalf("day = %+v %v", d, err)
	}
	if len(d.Events[0].Error) != maxDetailLen {
		t.Fatalf("error not truncated: %d", len(d.Events[0].Error))
	}
	r.Prune(ctx)
	days, _ = store.Days(ctx)
	if strings.Join(days, ",") != "2025-03-10,2025-03-09" {
		t.Fatalf("days after prune = %v", days)
	}
}

func TestRecorderDisabled(t *testing.T) {
	r := NewRecorder(NewMemoryStore(), 0, quiet)
	r.Record(Event{Action: "x"})
	if r.Enabled() || r.Retention() != 0 {
		t.Fatal("retention 0 must only log")
	}
	d, err := r.Get(context.Background(), "2025-03-10")
	if err != nil || len(d.Events) != 0 {
		t.Fatalf("day = %+v %v", d, err)
	}
}

func TestConfigMapStore(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "zea-audit-20250301", Namespace: "ns", Labels: map[string]string{LabelDay: "2025-03-01"}}})
	s := NewConfigMapStore(client, "ns")
	e1 := Event{Time: at("2025-03-10T10:00:00Z"), User: "a", Action: "one", Status: 200}
	e2 := Event{Time: at("2025-03-10T11:00:00Z"), User: "b", Action: "two", Status: 403, Error: "no"}
	if err := s.Append(ctx, "2025-03-10", []Event{e1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "2025-03-10", []Event{e2}); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get(ctx, "2025-03-10")
	if err != nil || len(d.Events) != 2 || d.Events[1].Error != "no" || !d.Events[0].Time.Equal(e1.Time) {
		t.Fatalf("day = %+v %v", d, err)
	}
	cm, _ := client.CoreV1().ConfigMaps("ns").Get(ctx, "zea-audit-20250310", metav1.GetOptions{})
	if cm.Labels[LabelType] != TypeAudit || cm.Labels[LabelDay] != "2025-03-10" {
		t.Fatalf("labels = %v", cm.Labels)
	}
	if err := s.Append(ctx, "2025-03-01", []Event{e1}); err == nil {
		t.Fatal("appending to a foreign ConfigMap must fail")
	}
	if _, err := s.Get(ctx, "2025-03-01"); err != ErrNotFound {
		t.Fatalf("foreign get err = %v", err)
	}
	days, _ := s.Days(ctx)
	if strings.Join(days, ",") != "2025-03-10" {
		t.Fatalf("days = %v", days)
	}

	big := Event{Time: e1.Time, User: "c", Action: "big", Error: strings.Repeat("y", 1000)}
	many := make([]Event, 1200)
	for i := range many {
		many[i] = big
	}
	if err := s.Append(ctx, "2025-03-10", many); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Get(ctx, "2025-03-10")
	if d.Dropped == 0 || d.Dropped+len(d.Events) != 1202 {
		t.Fatalf("dropped = %d events = %d", d.Dropped, len(d.Events))
	}
	if err := s.Delete(ctx, "2025-03-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "2025-03-10"); err != ErrNotFound {
		t.Fatalf("deleted get err = %v", err)
	}
}
