package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

const buildWorkflow = `on:
  push:
  workflow_dispatch:
    inputs:
      app_branch:
        description: Backend branch
        required: true
        default: master
      env:
        type: choice
        options: [dev, prod]
        default: dev
      dry_run:
        type: boolean
        default: false
      note:
`

func TestParseDispatchInputs(t *testing.T) {
	ok, inputs, err := parseDispatch([]byte(buildWorkflow))
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	got, _ := json.Marshal(inputs)
	want := `[{"name":"app_branch","description":"Backend branch","type":"string","required":true,"default":"master"},` +
		`{"name":"env","type":"choice","required":false,"default":"dev","options":["dev","prod"]},` +
		`{"name":"dry_run","type":"boolean","required":false,"default":"false"},` +
		`{"name":"note","type":"string","required":false}]`
	if string(got) != want {
		t.Fatalf("inputs =\n%s\nwant\n%s", got, want)
	}
	for _, src := range []string{"on: workflow_dispatch", "on:\n  workflow_dispatch:\n", "on: [workflow_dispatch]"} {
		ok, inputs, err := parseDispatch([]byte(src))
		if err != nil || !ok || len(inputs) != 0 {
			t.Errorf("%q: %v %v %v", src, ok, inputs, err)
		}
	}
}

func TestStatus(t *testing.T) {
	cases := map[[2]string]providers.Status{
		{"queued", ""}:                   providers.StatusQueued,
		{"waiting", ""}:                  providers.StatusManual,
		{"in_progress", ""}:              providers.StatusRunning,
		{"completed", "success"}:         providers.StatusSuccess,
		{"completed", "timed_out"}:       providers.StatusFailed,
		{"completed", "cancelled"}:       providers.StatusCanceled,
		{"completed", "skipped"}:         providers.StatusSkipped,
		{"completed", "action_required"}: providers.StatusManual,
		{"completed", "stale"}:           providers.StatusUnknown,
	}
	for in, want := range cases {
		if got := status(in[0], in[1]); got != want {
			t.Errorf("%v = %s, want %s", in, got, want)
		}
	}
}

const runJSONFixture = `{"id":77,"run_number":5,"workflow_id":1,"name":"Build","display_title":"Deploy","head_branch":"feature/x",
"head_sha":"abc","event":"workflow_dispatch","status":"completed","conclusion":"failure","html_url":"https://x/o/r/actions/runs/77",
"created_at":"2026-01-01T10:00:00Z","updated_at":"2026-01-01T10:05:00Z","actor":{"login":"bot"},"triggering_actor":{"login":"alice"}}`

func (f *fakeGitHub) runRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/workflows/{id}", f.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "1" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, `{"id":1,"name":"Build","path":".github/workflows/build.yml","state":"active"}`)
	}))
	mux.HandleFunc("POST /api/v3/repos/o/r/actions/workflows/{id}/dispatches", f.auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.dispatched = body
		inputs, _ := body["inputs"].(map[string]any)
		switch {
		case inputs["bogus"] != nil:
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Unexpected inputs provided: [\"bogus\"]"}`)
		case body["ref"] == "ghes" && body["return_run_details"] != nil:
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Invalid request.\n\n\"return_run_details\" is not a permitted key."}`)
		case body["ref"] == "ghes":
			w.WriteHeader(http.StatusNoContent)
		default:
			fmt.Fprint(w, `{"workflow_run_id":77,"run_url":"x","html_url":"https://x/o/r/actions/runs/77"}`)
		}
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/workflows/{id}/runs", f.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("branch") != "feature/x" || r.URL.Query().Get("per_page") != "5" {
			f.t.Errorf("workflow runs query = %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, runJSONFixture)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/runs", f.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "20" {
			f.t.Errorf("runs query = %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, runJSONFixture)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/runs/{run}", f.auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, runJSONFixture)
	}))
	mux.HandleFunc("GET /api/v3/repos/o/r/actions/runs/{run}/jobs", f.auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":9,"name":"build","status":"completed","conclusion":"failure",
"html_url":"https://x/job/9","started_at":"2026-01-01T10:00:10Z","completed_at":null,
"steps":[{"number":1,"name":"Checkout","status":"completed","conclusion":"success"},{"number":2,"name":"Build","status":"in_progress","conclusion":null}]}]}`)
	}))
	for _, action := range []string{"cancel", "rerun", "rerun-failed-jobs"} {
		mux.HandleFunc("POST /api/v3/repos/o/r/actions/runs/{run}/"+action, f.auth(func(w http.ResponseWriter, r *http.Request) {
			f.posted = append(f.posted, action+":"+r.PathValue("run"))
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{}`)
		}))
	}
}

func TestGitHubRuns(t *testing.T) {
	key := testKey(t)
	f := newFakeGitHub(t, &key.PublicKey)
	p := New(f.srv.Client())
	c := appConnection(t, f.srv.URL, key)
	ctx := context.Background()

	form, err := p.GetRunForm(ctx, c, "1", "feature/x")
	if err != nil || len(form.Inputs) != 4 || form.Variables {
		t.Fatalf("form: %+v %v", form, err)
	}
	if _, err := p.GetRunForm(ctx, c, "../1", "feature/x"); err == nil {
		t.Fatal("non-numeric workflow id accepted")
	}

	run, err := p.Trigger(ctx, c, providers.TriggerRequest{PipelineID: "1", Ref: "main", Inputs: map[string]string{"env": "prod"}})
	if err != nil || run.ID != "77" || run.WebURL == "" || run.Status != providers.StatusQueued {
		t.Fatalf("trigger: %+v %v", run, err)
	}
	if f.dispatched["ref"] != "main" || f.dispatched["inputs"].(map[string]any)["env"] != "prod" || f.dispatched["return_run_details"] != true {
		t.Fatalf("dispatch body = %v", f.dispatched)
	}

	run, err = p.Trigger(ctx, c, providers.TriggerRequest{PipelineID: "1", Ref: "ghes"})
	if err != nil || run.ID != "" || run.Ref != "ghes" {
		t.Fatalf("legacy trigger: %+v %v", run, err)
	}
	if _, ok := f.dispatched["return_run_details"]; ok {
		t.Fatal("legacy retry still sent return_run_details")
	}

	_, err = p.Trigger(ctx, c, providers.TriggerRequest{PipelineID: "1", Ref: "main", Inputs: map[string]string{"bogus": "1"}})
	if !providers.IsStatus(err, http.StatusUnprocessableEntity) {
		t.Fatalf("unknown input: %v", err)
	}
	if _, err := p.Trigger(ctx, c, providers.TriggerRequest{PipelineID: "1", Ref: "main", Variables: map[string]string{"X": "1"}}); err == nil {
		t.Fatal("free variables accepted by GitHub")
	}

	runs, err := p.ListRuns(ctx, c, providers.RunFilter{PipelineID: "1", Ref: "feature/x", Limit: 5})
	if err != nil || len(runs) != 1 {
		t.Fatalf("workflow runs: %+v %v", runs, err)
	}
	r := runs[0]
	if r.ID != "77" || r.Number != 5 || r.PipelineID != "1" || r.Actor != "alice" || r.Status != providers.StatusFailed || r.Title != "Deploy" {
		t.Fatalf("run = %+v", r)
	}
	if runs, err := p.ListRuns(ctx, c, providers.RunFilter{}); err != nil || len(runs) != 1 {
		t.Fatalf("repo runs: %+v %v", runs, err)
	}

	d, err := p.GetRun(ctx, c, "77")
	if err != nil || len(d.Jobs) != 1 {
		t.Fatalf("get run: %+v %v", d, err)
	}
	j := d.Jobs[0]
	if j.Status != providers.StatusFailed || j.StartedAt.IsZero() || !j.FinishedAt.IsZero() || len(j.Steps) != 2 || j.Steps[1].Status != providers.StatusRunning {
		t.Fatalf("job = %+v", j)
	}

	if err := p.CancelRun(ctx, c, "77"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := p.RetryRun(ctx, c, "77", false); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if err := p.RetryRun(ctx, c, "77", true); err != nil {
		t.Fatalf("rerun failed: %v", err)
	}
	if got := fmt.Sprint(f.posted); got != "[cancel:77 rerun:77 rerun-failed-jobs:77]" {
		t.Fatalf("posted = %s", got)
	}
	if f.writeTokens.Load() != 1 {
		t.Fatalf("write tokens = %d, want 1 (cached separately from read tokens)", f.writeTokens.Load())
	}
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
