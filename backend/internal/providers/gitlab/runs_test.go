package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

const ciWithInputs = `spec:
  inputs:
    environment:
      options: [dev, prod]
      default: dev
    replicas:
      type: number
      default: 1
    debug:
      type: boolean
      default: false
    tags:
      type: array
      default: [a, b]
    image:
      description: Image tag
---
build:
  script: echo $[[ inputs.image ]]
`

func TestSpecInputs(t *testing.T) {
	inputs, err := specInputs([]byte(ciWithInputs))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(inputs)
	want := `[{"name":"environment","type":"choice","required":false,"default":"dev","options":["dev","prod"]},` +
		`{"name":"replicas","type":"number","required":false,"default":"1"},` +
		`{"name":"debug","type":"boolean","required":false,"default":"false"},` +
		`{"name":"tags","type":"array","required":false,"default":"[\"a\",\"b\"]"},` +
		`{"name":"image","description":"Image tag","type":"string","required":true}]`
	if string(got) != want {
		t.Fatalf("inputs =\n%s\nwant\n%s", got, want)
	}
	for _, src := range []string{"", "build:\n  script: x\n", "stages: [a]\n---\nx: 1\n"} {
		inputs, err := specInputs([]byte(src))
		if err != nil || len(inputs) != 0 {
			t.Errorf("%q: %v %v", src, inputs, err)
		}
	}
}

const pipelineFixture = `{"id":900,"iid":12,"name":"Deploy","ref":"main","sha":"abc","status":"running","source":"api",
"web_url":"https://x/-/pipelines/900","created_at":"2026-01-01T10:00:00Z","updated_at":"2026-01-01T10:01:00Z","user":{"username":"bot"}}`

type fakeRuns struct {
	created map[string]any
	posted  []string
	// gqlNulls is how many GraphQL calls answer with a cache miss.
	gqlNulls int
	gqlError bool
	gqlCalls int
}

const ciWithVariables = `variables:
  DEPLOY_ENV:
    value: staging
    options: [staging, production]
    description: Target environment
  DEBUG:
    value: "false"
    description: Verbose logs
  INTERNAL: x
  PLAIN:
    value: y
build:
  script: echo
`

func TestFileVariables(t *testing.T) {
	for _, src := range []string{ciWithVariables, "spec:\n  inputs: {}\n---\n" + ciWithVariables} {
		vars, err := fileVariables([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(vars)
		want := `[{"name":"DEPLOY_ENV","description":"Target environment","type":"choice","required":false,"default":"staging","options":["staging","production"]},` +
			`{"name":"DEBUG","description":"Verbose logs","type":"string","required":false,"default":"false"}]`
		if string(got) != want {
			t.Fatalf("vars =\n%s\nwant\n%s", got, want)
		}
	}
	for _, src := range []string{"", ciWithInputs, "variables: [a]\n"} {
		vars, err := fileVariables([]byte(src))
		if err != nil || len(vars) != 0 {
			t.Errorf("%q: %v %v", src, vars, err)
		}
	}
}

func (f *fakeRuns) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path, query, _ := strings.Cut(r.RequestURI, "?")
		switch {
		case r.Method == http.MethodPost && path == "/api/graphql":
			var req struct {
				Variables map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Variables["fullPath"] != "grp/sub/proj" || req.Variables["ref"] != "main" {
				t.Errorf("graphql variables = %v", req.Variables)
			}
			f.gqlCalls++
			switch {
			case f.gqlError:
				fmt.Fprint(w, `{"data":{"project":null},"errors":[{"message":"denied"}]}`)
			case f.gqlCalls <= f.gqlNulls:
				fmt.Fprint(w, `{"data":{"project":{"ciConfigVariables":null}}}`)
			default:
				fmt.Fprint(w, `{"data":{"project":{"ciConfigVariables":[`+
					`{"key":"DEPLOY_ENV","value":"staging","description":"Target environment","valueOptions":["staging","production"]},`+
					`{"key":"FROM_INCLUDE","value":"1","description":null,"valueOptions":null}]}}}`)
			}
		case r.Method == http.MethodGet && path == projectPath:
			fmt.Fprint(w, `{"path_with_namespace":"grp/sub/proj","default_branch":"main","ci_config_path":""}`)
		case r.Method == http.MethodGet && path == projectPath+"/repository/files/.gitlab-ci.yml/raw":
			if !strings.Contains(query, "ref=main") {
				t.Errorf("raw file query = %q", query)
			}
			fmt.Fprint(w, ciWithInputs)
		case r.Method == http.MethodPost && path == projectPath+"/pipeline":
			_ = json.NewDecoder(r.Body).Decode(&f.created)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, pipelineFixture)
		case r.Method == http.MethodGet && path == projectPath+"/pipelines":
			if query != "per_page=20&ref=main" {
				t.Errorf("pipelines query = %q", query)
			}
			fmt.Fprintf(w, `[%s]`, pipelineFixture)
		case r.Method == http.MethodGet && path == projectPath+"/pipelines/900":
			fmt.Fprint(w, pipelineFixture)
		case r.Method == http.MethodGet && path == projectPath+"/pipelines/900/jobs":
			fmt.Fprint(w, `[{"id":2,"name":"deploy","stage":"deploy","status":"manual","web_url":"https://x/j/2","started_at":null,"finished_at":null},
{"id":1,"name":"build","stage":"build","status":"success","web_url":"https://x/j/1","started_at":"2026-01-01T10:00:05Z","finished_at":"2026-01-01T10:00:50Z"}]`)
		case r.Method == http.MethodPost && (path == projectPath+"/pipelines/900/cancel" || path == projectPath+"/pipelines/900/retry"):
			f.posted = append(f.posted, path[strings.LastIndex(path, "/")+1:])
			fmt.Fprint(w, pipelineFixture)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGitLabRuns(t *testing.T) {
	wait := configVariablesWait
	configVariablesWait = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { configVariablesWait = wait }()
	f := &fakeRuns{gqlNulls: 1}
	srv := f.server(t)
	defer srv.Close()
	p := New(srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: srv.URL + "/grp/sub/proj", Credentials: map[string]string{KeyToken: "glpat"}}
	ctx := context.Background()

	form, err := p.GetRunForm(ctx, c, projectPipelineID, "")
	if err != nil || !form.Variables || len(form.Inputs) != 5 || form.Warning != "" {
		t.Fatalf("form: %+v %v", form, err)
	}
	prefilled, _ := json.Marshal(form.PrefilledVariables)
	wantPrefilled := `[{"name":"DEPLOY_ENV","description":"Target environment","type":"choice","required":false,"default":"staging","options":["staging","production"]},` +
		`{"name":"FROM_INCLUDE","type":"string","required":false,"default":"1"}]`
	if string(prefilled) != wantPrefilled || f.gqlCalls != 2 {
		t.Fatalf("prefilled after %d calls =\n%s\nwant\n%s", f.gqlCalls, prefilled, wantPrefilled)
	}

	// GraphQL stays on a cache miss: the form still opens with a warning.
	f.gqlCalls, f.gqlNulls = 0, 10
	form, err = p.GetRunForm(ctx, c, projectPipelineID, "main")
	if err != nil || len(form.PrefilledVariables) != 0 || !strings.Contains(form.Warning, "not prepared") || f.gqlCalls != 3 {
		t.Fatalf("cache miss form after %d calls: %+v %v", f.gqlCalls, form, err)
	}
	f.gqlError = true
	form, err = p.GetRunForm(ctx, c, projectPipelineID, "main")
	if err != nil || len(form.Inputs) != 5 || !strings.Contains(form.Warning, "denied") {
		t.Fatalf("graphql error form: %+v %v", form, err)
	}
	f.gqlError, f.gqlNulls = false, 0
	if _, err := p.GetRunForm(ctx, c, "other", "main"); err == nil {
		t.Fatal("unknown pipeline id accepted")
	}

	calls := f.gqlCalls
	run, err := p.Trigger(ctx, c, providers.TriggerRequest{
		PipelineID: projectPipelineID,
		Ref:        "main",
		Variables:  map[string]string{"APP_BRANCH": "dev"},
		Inputs:     map[string]string{"replicas": "3", "debug": "true", "tags": `["x"]`, "image": "v1"},
	})
	if err != nil || run.ID != "900" || run.Number != 12 || run.Status != providers.StatusRunning || run.Actor != "bot" {
		t.Fatalf("trigger: %+v %v", run, err)
	}
	body, _ := json.Marshal(f.created)
	want := `{"inputs":{"debug":true,"image":"v1","replicas":3,"tags":["x"]},"ref":"main","variables":[{"key":"APP_BRANCH","value":"dev","variable_type":"env_var"}]}`
	if string(body) != want {
		t.Fatalf("create body =\n%s\nwant\n%s", body, want)
	}
	if f.gqlCalls != calls {
		t.Fatal("trigger queried prefilled variables")
	}
	for _, bad := range []providers.TriggerRequest{
		{PipelineID: projectPipelineID, Ref: "main", Inputs: map[string]string{"unknown": "1"}},
		{PipelineID: projectPipelineID, Ref: "main", Inputs: map[string]string{"replicas": "many"}},
		{PipelineID: projectPipelineID, Ref: "main", Variables: map[string]string{"BAD-KEY": "1"}},
		{PipelineID: projectPipelineID},
	} {
		if _, err := p.Trigger(ctx, c, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}

	runs, err := p.ListRuns(ctx, c, providers.RunFilter{PipelineID: projectPipelineID, Ref: "main"})
	if err != nil || len(runs) != 1 || runs[0].Title != "Deploy" {
		t.Fatalf("runs: %+v %v", runs, err)
	}

	d, err := p.GetRun(ctx, c, "900")
	if err != nil || len(d.Jobs) != 2 {
		t.Fatalf("get run: %+v %v", d, err)
	}
	if d.Jobs[0].Name != "build" || d.Jobs[0].Status != providers.StatusSuccess || d.Jobs[1].Status != providers.StatusManual || !d.Jobs[1].StartedAt.IsZero() {
		t.Fatalf("jobs = %+v", d.Jobs)
	}

	if err := p.CancelRun(ctx, c, "900"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := p.RetryRun(ctx, c, "900", true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := p.RetryRun(ctx, c, "900", false); err == nil {
		t.Fatal("full rerun should be unsupported")
	}
	if got := fmt.Sprint(f.posted); got != "[cancel retry]" {
		t.Fatalf("posted = %s", got)
	}
}
