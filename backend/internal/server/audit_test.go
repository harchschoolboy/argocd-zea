package server

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestAuditRecordsChanges(t *testing.T) {
	e := newEnv(false)
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/shared", nil, as("admin", "")); code != http.StatusNoContent {
		t.Fatalf("delete status = %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/connections/secret", nil); code < 400 {
		t.Fatalf("forbidden delete status = %d", code)
	}
	e.do(t, "POST", "/api/v1/images/match", map[string]any{"images": []string{"x"}})
	e.do(t, "GET", "/api/v1/connections", nil)

	if code, _ := e.do(t, "GET", "/api/v1/audit", nil); code != http.StatusForbidden {
		t.Fatalf("non-admin audit status = %d", code)
	}
	e.audit.Flush(context.Background())
	code, out := e.do(t, "GET", "/api/v1/audit", nil, as("admin", ""))
	if code != http.StatusOK {
		t.Fatalf("audit status = %d %v", code, out)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if out["day"] != today || out["retention"] != float64(30) {
		t.Fatalf("audit day = %v retention = %v", out["day"], out["retention"])
	}
	events := out["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	failed, ok := events[0].(map[string]any), events[1].(map[string]any)
	if ok["user"] != "admin" || ok["action"] != "connection.delete" || ok["resource"] != "connection" || ok["name"] != "shared" || ok["status"] != float64(204) {
		t.Fatalf("delete event = %v", ok)
	}
	if failed["user"] != "alice" || failed["name"] != "secret" || failed["status"].(float64) < 400 || failed["error"] == "" {
		t.Fatalf("failed event = %v", failed)
	}
	if code, _ := e.do(t, "GET", "/api/v1/audit?day=yesterday", nil, as("admin", "")); code != http.StatusBadRequest {
		t.Fatalf("bad day status = %d", code)
	}
}

func TestAuditNamesCreatedStream(t *testing.T) {
	e := newEnv(false)
	body := map[string]any{"name": "fresh", "stages": []any{map[string]any{"steps": []any{
		map[string]any{"id": "a", "connection": "shared", "pipeline": "1", "ref": "main"}}}}}
	code, out := e.do(t, "POST", "/api/v1/streams", body, as("admin", ""))
	if code != http.StatusCreated {
		t.Fatalf("create status = %d %v", code, out)
	}
	d, err := e.audit.Get(context.Background(), time.Now().UTC().Format("2006-01-02"))
	if err != nil || len(d.Events) != 1 {
		t.Fatalf("events = %v %v", d, err)
	}
	if ev := d.Events[0]; ev.Action != "stream.create" || ev.Name != "fresh" || ev.Status != http.StatusCreated {
		t.Fatalf("event = %+v", ev)
	}
}
