package streams

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// fakeCI is a scripted provider: runs stay queued until the test finishes
// them.
type fakeCI struct {
	mu          sync.Mutex
	now         func() time.Time
	next        int
	runs        map[string]*providers.Run
	triggers    []providers.TriggerRequest
	cancelled   []string
	noID        bool
	failTrigger map[string]bool
}

func newFakeCI(now func() time.Time) *fakeCI {
	return &fakeCI{now: now, runs: map[string]*providers.Run{}, failTrigger: map[string]bool{}}
}

func (f *fakeCI) Info() providers.Info                   { return providers.Info{ID: "fake", Name: "Fake"} }
func (f *fakeCI) Validate(*connections.Connection) error { return nil }
func (f *fakeCI) Test(context.Context, *connections.Connection) (*providers.Repository, error) {
	return &providers.Repository{}, nil
}
func (f *fakeCI) ListBranches(context.Context, *connections.Connection) (*providers.BranchList, error) {
	return &providers.BranchList{}, nil
}
func (f *fakeCI) ListPipelines(context.Context, *connections.Connection, string) ([]providers.Pipeline, error) {
	return nil, nil
}
func (f *fakeCI) GetRunForm(context.Context, *connections.Connection, string, string) (*providers.RunForm, error) {
	return &providers.RunForm{}, nil
}
func (f *fakeCI) RetryRun(context.Context, *connections.Connection, string, bool) error { return nil }

func (f *fakeCI) Trigger(_ context.Context, c *connections.Connection, req providers.TriggerRequest) (*providers.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers = append(f.triggers, req)
	if f.failTrigger[req.PipelineID] {
		return nil, &providers.UpstreamError{Provider: "Fake", Status: 422, Message: "bad input"}
	}
	f.next++
	id := strconv.Itoa(f.next)
	r := &providers.Run{ID: id, PipelineID: req.PipelineID, Ref: req.Ref, Status: providers.StatusQueued,
		CommitSHA: "sha-" + id, WebURL: "https://ci/" + c.Name + "/" + id, CreatedAt: f.now()}
	f.runs[id] = r
	if f.noID {
		return &providers.Run{Status: providers.StatusQueued}, nil
	}
	cp := *r
	return &cp, nil
}

func (f *fakeCI) ListRuns(_ context.Context, _ *connections.Connection, flt providers.RunFilter) ([]providers.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []providers.Run
	for _, r := range f.runs {
		if (flt.PipelineID == "" || r.PipelineID == flt.PipelineID) && (flt.Ref == "" || r.Ref == flt.Ref) {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeCI) GetRun(_ context.Context, _ *connections.Connection, id string) (*providers.RunDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return nil, &providers.UpstreamError{Provider: "Fake", Status: 404, Message: "not found"}
	}
	return &providers.RunDetail{Run: *r}, nil
}

func (f *fakeCI) CancelRun(_ context.Context, _ *connections.Connection, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	if r, ok := f.runs[id]; ok {
		r.Status = providers.StatusCanceled
	}
	return nil
}

// finish sets the status of the latest run of a pipeline.
func (f *fakeCI) finish(t *testing.T, pipeline string, st providers.Status) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	best := 0
	for id, r := range f.runs {
		if n, _ := strconv.Atoi(id); r.PipelineID == pipeline && n > best {
			best = n
		}
	}
	if best == 0 {
		t.Fatalf("no run of pipeline %q", pipeline)
	}
	f.runs[strconv.Itoa(best)].Status = st
}

func (f *fakeCI) triggered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.triggers))
	for _, t := range f.triggers {
		out = append(out, t.PipelineID)
	}
	return out
}

func (f *fakeCI) lastTrigger(pipeline string) providers.TriggerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.triggers) - 1; i >= 0; i-- {
		if f.triggers[i].PipelineID == pipeline {
			return f.triggers[i]
		}
	}
	return providers.TriggerRequest{}
}

type engineEnv struct {
	e     *Engine
	ci    *fakeCI
	runs  *MemoryRunStore
	conns *connections.MemoryStore
	clock time.Time
}

func newEngineEnv(cfg EngineConfig) *engineEnv {
	env := &engineEnv{clock: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC), runs: NewMemoryRunStore()}
	now := func() time.Time { return env.clock }
	env.ci = newFakeCI(now)
	env.conns = connections.NewMemoryStore(
		&connections.Connection{Name: "app", Provider: "fake"},
		&connections.Connection{Name: "infra", Provider: "fake"},
	)
	env.e = NewEngine(env.runs, env.conns, providers.NewRegistry(env.ci), cfg, nil)
	env.e.now = now
	return env
}

func (env *engineEnv) advance(d time.Duration) { env.clock = env.clock.Add(d) }

func (env *engineEnv) start(t *testing.T, st *Stream, params map[string]string) *Run {
	t.Helper()
	r, err := env.e.Start(context.Background(), st, params, "alice")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return r
}

func (env *engineEnv) tick(t *testing.T, r *Run) *Run {
	t.Helper()
	env.e.ReconcileAll(context.Background())
	cur, err := env.runs.Get(context.Background(), r.Stream, r.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return cur
}

func stepStatus(r *Run, id string) StepState {
	for _, s := range r.Steps {
		if s.ID == id {
			return s
		}
	}
	return StepState{}
}

func wantSteps(t *testing.T, r *Run, want map[string]string) {
	t.Helper()
	for id, st := range want {
		if got := stepStatus(r, id); got.Status != st {
			t.Errorf("step %s = %s (%s), want %s", id, got.Status, got.Message, st)
		}
	}
}

func step(id, conn, pipeline string) Step {
	return Step{ID: id, Connection: conn, Pipeline: pipeline, Ref: "main"}
}

func flow(stages ...Stage) *Stream {
	return &Stream{Name: "flow", Spec: Spec{Stages: stages}}
}

func TestEngineOrderAndTemplates(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	buildA := step("build-a", "app", "build.yml")
	buildA.Ref = "${{ params.branch }}"
	buildB := step("build-b", "infra", "build-infra.yml")
	deploy := step("deploy", "infra", "deploy.yml")
	deploy.Inputs = map[string]string{"sha": "${{ steps.build-a.sha }}", "by": "${{ zea.user }}", "ref": "${{ steps.build-a.ref }}"}
	st := flow(Stage{Steps: []Step{buildA, buildB}}, Stage{Steps: []Step{deploy}})
	st.Params = []Param{{Name: "branch", Type: ParamBranch, Connection: "app", Default: "main"}}

	r := env.start(t, st, map[string]string{"branch": "feature/x"})
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build-a": StepRunning, "build-b": StepRunning, "deploy": StepPending})
	if got := env.ci.lastTrigger("build.yml").Ref; got != "feature/x" {
		t.Fatalf("build ref = %q", got)
	}

	env.ci.finish(t, "build.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build-a": StepSucceeded, "build-b": StepRunning, "deploy": StepPending})

	env.ci.finish(t, "build-infra.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build-b": StepSucceeded, "deploy": StepRunning})
	in := env.ci.lastTrigger("deploy.yml").Inputs
	if in["sha"] != "sha-1" || in["by"] != "alice" || in["ref"] != "feature/x" {
		t.Fatalf("deploy inputs = %v", in)
	}

	env.ci.finish(t, "deploy.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	if r.Status != RunSucceeded || r.FinishedAt.IsZero() {
		t.Fatalf("run = %s, finished %v", r.Status, r.FinishedAt)
	}
	if active, _ := env.runs.ListActive(context.Background()); len(active) != 0 {
		t.Fatalf("active runs = %d", len(active))
	}
}

func TestEngineNeedsSkipsStageBarrier(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	fast := step("fast", "app", "fast.yml")
	slow := step("slow", "app", "slow.yml")
	after := step("after-fast", "app", "after.yml")
	after.Needs = []string{"fast"}
	r := env.start(t, flow(Stage{Steps: []Step{fast, slow}}, Stage{Steps: []Step{after}}), nil)
	r = env.tick(t, r)
	env.ci.finish(t, "fast.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"slow": StepRunning, "after-fast": StepRunning})
}

func TestEngineConditions(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	notify := step("notify", "app", "notify.yml")
	notify.When = WhenFailure
	cleanup := step("cleanup", "app", "cleanup.yml")
	cleanup.When = WhenAlways
	st := flow(
		Stage{Steps: []Step{step("build", "app", "build.yml")}},
		Stage{Steps: []Step{step("deploy", "app", "deploy.yml"), notify, cleanup}},
		Stage{Steps: []Step{step("smoke", "app", "smoke.yml")}},
	)
	r := env.start(t, st, nil)
	r = env.tick(t, r)
	env.ci.finish(t, "build.yml", providers.StatusFailed)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepFailed, "deploy": StepSkipped, "notify": StepRunning, "cleanup": StepRunning, "smoke": StepPending})

	env.ci.finish(t, "notify.yml", providers.StatusSuccess)
	env.ci.finish(t, "cleanup.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"smoke": StepSkipped})
	if r.Status != RunFailed || !strings.Contains(r.Message, `"build"`) {
		t.Fatalf("run = %s %q", r.Status, r.Message)
	}
	if !strings.Contains(stepStatus(r, "smoke").Message, `"deploy" was skipped`) {
		t.Fatalf("smoke message = %q", stepStatus(r, "smoke").Message)
	}
}

func TestEngineFailureStepSkippedOnSuccess(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	notify := step("notify", "app", "notify.yml")
	notify.When = WhenFailure
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}, Stage{Steps: []Step{notify}}), nil)
	r = env.tick(t, r)
	env.ci.finish(t, "build.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"notify": StepSkipped})
	if r.Status != RunSucceeded {
		t.Fatalf("run = %s", r.Status)
	}
}

func TestEngineContinueOnError(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	lint := step("lint", "app", "lint.yml")
	lint.ContinueOnError = true
	r := env.start(t, flow(Stage{Steps: []Step{lint}}, Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	r = env.tick(t, r)
	env.ci.finish(t, "lint.yml", providers.StatusFailed)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"lint": StepFailed, "build": StepRunning})
	env.ci.finish(t, "build.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	if r.Status != RunSucceeded {
		t.Fatalf("run = %s %q", r.Status, r.Message)
	}
}

func TestEngineTriggerError(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	env.ci.failTrigger["build.yml"] = true
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}, Stage{Steps: []Step{step("deploy", "app", "deploy.yml")}}), nil)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepFailed, "deploy": StepSkipped})
	if !strings.Contains(stepStatus(r, "build").Message, "trigger failed") || r.Status != RunFailed {
		t.Fatalf("build = %+v, run = %s", stepStatus(r, "build"), r.Status)
	}
}

func TestEngineMissingConnectionFailsStep(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "gone", "build.yml")}}), nil)
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepFailed || !strings.Contains(s.Message, "gone") {
		t.Fatalf("build = %+v", s)
	}
}

func TestEngineCorrelatesRunWithoutID(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	env.ci.noID = true
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepStarting || s.TriggeredAt.IsZero() {
		t.Fatalf("after trigger = %+v", s)
	}
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepRunning || s.RunID != "1" || s.SHA != "sha-1" {
		t.Fatalf("after correlate = %+v", s)
	}
	if n := len(env.ci.triggered()); n != 1 {
		t.Fatalf("triggers = %d", n)
	}
}

func TestEngineStartingStepFailsAfterGrace(t *testing.T) {
	env := newEngineEnv(EngineConfig{StartGrace: time.Minute})
	env.ci.noID = true
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	r = env.tick(t, r)
	env.ci.mu.Lock()
	env.ci.runs = map[string]*providers.Run{}
	env.ci.mu.Unlock()
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepStarting})
	env.advance(2 * time.Minute)
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepFailed || !strings.Contains(s.Message, "could not be found") {
		t.Fatalf("build = %+v", s)
	}
}

func TestEngineTimeout(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	build := step("build", "app", "build.yml")
	build.Timeout = "5m"
	r := env.start(t, flow(Stage{Steps: []Step{build}}), nil)
	r = env.tick(t, r)
	env.advance(4 * time.Minute)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepRunning})
	env.advance(2 * time.Minute)
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepFailed || !strings.Contains(s.Message, "timed out after 5m") {
		t.Fatalf("build = %+v", s)
	}
	if len(env.ci.cancelled) != 1 || r.Status != RunFailed {
		t.Fatalf("cancelled = %v, run = %s", env.ci.cancelled, r.Status)
	}
}

func TestEngineManualKeepsRunning(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	r = env.tick(t, r)
	env.ci.finish(t, "build.yml", providers.StatusManual)
	r = env.tick(t, r)
	if s := stepStatus(r, "build"); s.Status != StepRunning || s.ProviderStatus != "manual" || s.Message == "" {
		t.Fatalf("build = %+v", s)
	}
}

func TestEngineCancel(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}, Stage{Steps: []Step{step("deploy", "app", "deploy.yml")}}), nil)
	r = env.tick(t, r)
	if _, err := env.e.Cancel(context.Background(), r.Stream, r.ID, "bob"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepCancelled, "deploy": StepSkipped})
	if r.Status != RunCancelled || r.Message != "cancelled by bob" || len(env.ci.cancelled) != 1 {
		t.Fatalf("run = %s %q, provider cancels = %v", r.Status, r.Message, env.ci.cancelled)
	}
	if _, err := env.e.Cancel(context.Background(), r.Stream, r.ID, "bob"); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("second cancel = %v", err)
	}
}

func TestEngineResumesWithNewInstance(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}, Stage{Steps: []Step{step("deploy", "app", "deploy.yml")}}), nil)
	r = env.tick(t, r)
	restarted := NewEngine(env.runs, env.conns, providers.NewRegistry(env.ci), EngineConfig{}, nil)
	restarted.now = env.e.now
	env.e = restarted
	env.ci.finish(t, "build.yml", providers.StatusSuccess)
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"build": StepSucceeded, "deploy": StepRunning})
}

func TestEngineSaveKeepsConcurrentCancel(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	stale := cloneRun(r)
	if _, err := env.e.Cancel(context.Background(), r.Stream, r.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if !env.e.setStep(context.Background(), stale, 0, func(s *StepState) { s.Message = "x" }) {
		t.Fatal("setStep failed")
	}
	if !stale.CancelRequested || stale.Steps[0].Message != "x" {
		t.Fatalf("after save = %+v", stale)
	}
}

func TestEngineDoesNotTriggerStaleStep(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	r := env.start(t, flow(Stage{Steps: []Step{step("build", "app", "build.yml")}}), nil)
	stale := cloneRun(r)
	env.tick(t, r)
	env.e.start(context.Background(), stale, 0, stale.StepDefs()["build"])
	if n := len(env.ci.triggered()); n != 1 {
		t.Fatalf("triggers = %d, want 1", n)
	}
}

func TestEngineRejectsInvalidStart(t *testing.T) {
	env := newEngineEnv(EngineConfig{})
	if _, err := env.e.Start(context.Background(), flow(), nil, "a"); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("empty stream = %v", err)
	}
	st := flow(Stage{Steps: []Step{step("build", "app", "build.yml")}})
	if _, err := env.e.Start(context.Background(), st, map[string]string{"nope": "1"}, "a"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("unknown param = %v", err)
	}
}

func TestResolveParams(t *testing.T) {
	defs := []Param{
		{Name: "branch", Type: ParamBranch, Connection: "app", Default: "main"},
		{Name: "env", Type: ParamChoice, Options: []string{"dev", "prod"}, Required: true},
		{Name: "dry", Type: ParamBoolean},
		{Name: "note", Type: ParamString},
	}
	got, err := ResolveParams(defs, map[string]string{"env": "prod", "branch": " dev "})
	if err != nil {
		t.Fatal(err)
	}
	if got["branch"] != "dev" || got["env"] != "prod" || got["dry"] != "false" || got["note"] != "" {
		t.Fatalf("resolved = %v", got)
	}
	for name, given := range map[string]map[string]string{
		"missing required": {},
		"bad choice":       {"env": "qa"},
		"bad boolean":      {"env": "dev", "dry": "yes"},
		"unknown":          {"env": "dev", "x": "1"},
	} {
		if _, err := ResolveParams(defs, given); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRunHistoryIsPruned(t *testing.T) {
	env := newEngineEnv(EngineConfig{History: 2})
	st := flow(Stage{Steps: []Step{step("build", "app", "build.yml")}})
	for i := 0; i < 3; i++ {
		r := env.start(t, st, nil)
		env.tick(t, r)
		env.ci.finish(t, "build.yml", providers.StatusSuccess)
		env.tick(t, r)
		env.advance(time.Second)
	}
	running := env.start(t, st, nil)
	runs, _ := env.runs.List(context.Background(), "flow")
	if len(runs) != 3 || runs[0].ID != running.ID {
		t.Fatalf("runs = %d, newest = %s", len(runs), runs[0].ID)
	}
}

func TestConfigMapRunStore(t *testing.T) {
	ctx := context.Background()
	s := NewConfigMapRunStore(fake.NewClientset(), "zea")
	st := flow(Stage{Steps: []Step{step("build", "app", "build.yml")}})
	r, err := NewRun(st, nil, "alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if active, _ := s.ListActive(ctx); len(active) != 1 {
		t.Fatalf("active = %d", len(active))
	}
	created.Status = RunSucceeded
	if _, err := s.Update(ctx, created); err != nil {
		t.Fatalf("update: %v", err)
	}
	if active, _ := s.ListActive(ctx); len(active) != 0 {
		t.Fatalf("active after finish = %d", len(active))
	}
	got, err := s.Get(ctx, "flow", r.ID)
	if err != nil || got.Status != RunSucceeded || len(got.Spec.Stages) != 1 || got.Steps[0].ID != "build" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if all, _ := s.List(ctx, "flow"); len(all) != 1 {
		t.Fatalf("list = %d", len(all))
	}
	if _, err := s.Get(ctx, "other", r.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("other stream = %v", err)
	}
	if _, err := s.Get(ctx, "flow", "../x"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("bad id = %v", err)
	}
	if err := s.Delete(ctx, "flow", r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "flow", r.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestRunIDs(t *testing.T) {
	now := time.Now()
	a, _ := newRunID(now)
	b, _ := newRunID(now.Add(time.Millisecond))
	if !ValidRunID(a) || !ValidRunID(b) || a >= b {
		t.Fatalf("ids %q %q", a, b)
	}
}
