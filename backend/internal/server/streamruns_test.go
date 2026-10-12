package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
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
	if code, _ := e.do(t, "GET", "/api/v1/streams/pub/runs", nil, bob); code != http.StatusNotFound {
		t.Fatalf("bob lists runs: %d", code)
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

func TestStreamRunRetry(t *testing.T) {
	e := newEnv(false)
	_, body := e.do(t, "POST", "/api/v1/streams/pub/runs", map[string]any{"params": map[string]string{"branch": "dev"}})
	id := body["id"].(string)
	e.eng.ReconcileAll(context.Background())
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/retry", nil); code != http.StatusConflict {
		t.Fatalf("retry while running: %d", code)
	}
	e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/cancel", nil)
	e.eng.ReconcileAll(context.Background())
	if code, _ := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/retry", nil, as("bob", "others")); code != http.StatusNotFound {
		t.Fatalf("bob retry: %d", code)
	}
	code, body := e.do(t, "POST", "/api/v1/streams/pub/runs/"+id+"/retry", nil)
	if code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, body)
	}
	attempts, _ := body["attempts"].([]any)
	if body["status"] != "running" || body["attempt"] != float64(2) || body["retriedBy"] != "alice" || len(attempts) != 1 {
		t.Fatalf("retry body = %v", body)
	}
	_, body = e.do(t, "GET", "/api/v1/streams/pub/runs", nil)
	if run := body["runs"].([]any)[0].(map[string]any); run["attempt"] != float64(2) || run["attempts"] != nil {
		t.Fatalf("listed run = %v", run)
	}
}

func TestStreamRunApproval(t *testing.T) {
	e := newEnv(false)
	ctx := context.Background()
	gate := streams.Step{ID: "gate", Type: streams.StepTypeApproval, Message: "Ship ${{ steps.build.ref }}?", NoSelfApproval: true}
	if _, err := e.strms.Create(ctx, &streams.Stream{Name: "gated", Editable: true, Spec: streams.Spec{Stages: []streams.Stage{
		{Steps: []streams.Step{step("build", "shared", "main")}}, {Steps: []streams.Step{gate}}, {Steps: []streams.Step{step("deploy", "gitops", "main")}},
	}}}); err != nil {
		t.Fatal(err)
	}
	e.policy.SetRaw(`roles:
  - name: dev
    rules:
      - {resource: streams, pattern: gated, actions: [run]}
      - {resource: connections, pattern: "*", actions: [run]}
  - name: approver
    rules:
      - {resource: streams, pattern: gated, actions: [approve]}
bindings:
  - {group: devs, roles: [dev]}
  - {user: alice, roles: [approver]}
  - {user: carol, roles: [approver]}
`, false)
	code, body := e.do(t, "POST", "/api/v1/streams/gated/runs", nil)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	id := body["id"].(string)
	base := "/api/v1/streams/gated/runs/" + id + "/steps/gate/"
	if code, _ := e.do(t, "POST", base+"approve", nil, as("carol", "")); code != http.StatusConflict {
		t.Fatalf("approve before waiting: %d", code)
	}
	// build: finish it by hand; the fake provider keeps runs running.
	if _, err := streams.Mutate(ctx, e.eng.Runs(), "gated", id, func(r *streams.Run) error {
		r.Steps[0].Status = streams.StepSucceeded
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.eng.ReconcileAll(ctx)
	_, body = e.do(t, "GET", "/api/v1/streams/gated/runs/"+id, nil)
	g := body["steps"].([]any)[1].(map[string]any)
	if g["status"] != "waiting" || g["type"] != "approval" || g["noSelfApproval"] != true || g["prompt"] != "Ship ?" {
		t.Fatalf("gate = %v", g)
	}
	if code, _ := e.do(t, "POST", base+"approve", nil, as("bob", "devs")); code != http.StatusForbidden {
		t.Fatalf("bob approves without the approve action: %d", code)
	}
	if code, _ := e.do(t, "POST", base+"approve", nil); code != http.StatusForbidden {
		t.Fatalf("self approval: %d", code)
	}
	if code, _ := e.do(t, "POST", base+"approve", map[string]any{"nope": 1}, as("carol", "")); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	code, body = e.do(t, "POST", base+"reject", map[string]any{"comment": "wait for QA"}, as("carol", ""))
	if code != http.StatusOK {
		t.Fatalf("reject: %d %v", code, body)
	}
	g = body["steps"].([]any)[1].(map[string]any)
	if g["status"] != "failed" || g["decidedBy"] != "carol" || g["comment"] != "wait for QA" {
		t.Fatalf("rejected gate = %v", g)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/gated/runs/"+id+"/steps/build/approve", nil, as("carol", "")); code != http.StatusConflict {
		t.Fatalf("approve a pipeline step: %d", code)
	}
}
