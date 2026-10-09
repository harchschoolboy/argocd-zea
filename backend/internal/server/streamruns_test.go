package server

import (
	"context"
	"net/http"
	"testing"
)

func TestStreamRunLifecycle(t *testing.T) {
	e := newEnv(false)
	code, body := e.do(t, "POST", "/api/v1/streams/pub/runs", map[string]any{"params": map[string]string{"branch": "dev"}})
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	id := body["id"].(string)
	if body["status"] != "running" || body["user"] != "alice" || body["spec"] == nil || len(body["steps"].([]any)) != 2 {
		t.Fatalf("start body = %v", body)
	}

	e.eng.ReconcileAll(context.Background())
	if lastTrigger.Ref != "dev" {
		t.Fatalf("trigger ref = %q", lastTrigger.Ref)
	}
	code, body = e.do(t, "GET", "/api/v1/streams/pub/runs/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}
	first := body["steps"].([]any)[0].(map[string]any)
	if first["status"] != "running" || first["runId"] != "42" || first["connection"] != "shared" || first["ref"] != "dev" {
		t.Fatalf("first step = %v", first)
	}

	code, body = e.do(t, "GET", "/api/v1/streams/pub/runs", nil)
	if runs := body["runs"].([]any); code != http.StatusOK || len(runs) != 1 || runs[0].(map[string]any)["spec"] != nil {
		t.Fatalf("list: %d %v", code, body)
	}

	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/cancel", nil); code != http.StatusAccepted {
		t.Fatalf("cancel: %d", code)
	}
	e.eng.ReconcileAll(context.Background())
	_, body = e.do(t, "GET", "/api/v1/streams/pub/runs/"+id, nil)
	if body["status"] != "cancelled" || body["message"] != "cancelled by alice" {
		t.Fatalf("after cancel = %v", body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/cancel", nil); code != http.StatusConflict {
		t.Fatalf("second cancel: %d", code)
	}
}

func TestStreamRunAccess(t *testing.T) {
	e := newEnv(false)
	if code, _ := e.do(t, "POST", "/api/v1/streams/ops-only/runs", nil); code != http.StatusNotFound {
		t.Fatalf("hidden stream start: %d", code)
	}
	code, body := e.do(t, "POST", "/api/v1/streams/pub/runs", map[string]any{"params": map[string]string{"branch": "dev"}})
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	id := body["id"].(string)

	bob := as("bob", "others")
	_, body = e.do(t, "GET", "/api/v1/streams/pub/runs", nil, bob)
	if runs := body["runs"].([]any); len(runs) != 0 {
		t.Fatalf("bob sees %d runs", len(runs))
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs/"+id, nil, bob); code != http.StatusNotFound {
		t.Fatalf("bob get: %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/cancel", nil, bob); code != http.StatusNotFound {
		t.Fatalf("bob cancel: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs/"+id, nil, as("admin", "")); code != http.StatusOK {
		t.Fatalf("admin get: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs/nope-1", nil); code != http.StatusNotFound {
		t.Fatalf("missing run: %d", code)
	}
}

func TestStreamRunValidation(t *testing.T) {
	e := newEnv(false)
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs", map[string]any{"params": map[string]string{"nope": "1"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown param: %d", code)
	}
	code, body := e.do(t, "POST", "/api/v1/streams/empty/runs", nil, as("admin", ""))
	if code != http.StatusConflict {
		t.Fatalf("stream with problems: %d %v", code, body)
	}
	if err := e.store.Delete(context.Background(), "shared"); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs", nil, as("admin", "")); code != http.StatusConflict {
		t.Fatalf("missing connection: %d", code)
	}
}
