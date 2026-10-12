package streams

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeApps struct {
	mu        sync.Mutex
	apps      map[string]AppState
	refreshed []string
}

func newFakeApps() *fakeApps { return &fakeApps{apps: map[string]AppState{}} }

func (f *fakeApps) set(app string, st AppState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app] = st
}

func (f *fakeApps) GetApp(_ context.Context, namespace, name string) (*AppState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.apps[namespace+"/"+name]
	if !ok {
		return nil, ErrAppNotFound
	}
	return &st, nil
}

func (f *fakeApps) RefreshApp(_ context.Context, namespace, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, namespace+"/"+name)
	if _, ok := f.apps[namespace+"/"+name]; !ok {
		return ErrAppNotFound
	}
	return nil
}

type forbiddenApps struct{}

func (forbiddenApps) GetApp(context.Context, string, string) (*AppState, error) {
	return nil, ErrAppForbidden
}
func (forbiddenApps) RefreshApp(context.Context, string, string) error { return ErrAppForbidden }

func argoStep(id, app, image string) Step {
	return Step{ID: id, Type: StepTypeArgoCD, App: app, Image: image}
}

func newArgoEnv() (*engineEnv, *fakeApps) {
	apps := newFakeApps()
	return newEngineEnv(EngineConfig{Apps: apps, AppNamespaces: []string{"argocd", "apps"}}), apps
}

func TestEngineArgoImage(t *testing.T) {
	env, apps := newArgoEnv()
	apps.set("argocd/web", AppState{Sync: "Synced", Health: "Healthy", Images: []string{"reg.io/web:v1"}})
	wait := argoStep("wait", "${{ params.app }}", "${{ params.tag }}")
	notify := step("notify", "app", "notify.yml")
	notify.Inputs = map[string]string{"health": "${{ steps.wait.health }}", "rev": "${{ steps.wait.revision }}", "images": "${{ steps.wait.images }}"}
	st := flow(Stage{Steps: []Step{wait}}, Stage{Steps: []Step{notify}})
	st.Params = []Param{{Name: "app", Type: ParamString}, {Name: "tag", Type: ParamImage, Connection: "app"}}
	r := env.start(t, st, map[string]string{"app": "web", "tag": "v2"})

	r = env.tick(t, r)
	s := stepStatus(r, "wait")
	if s.Status != StepRunning || s.App != "argocd/web" || s.Image != "v2" || !strings.Contains(s.Message, "waiting for image v2") {
		t.Fatalf("wait = %+v", s)
	}
	if len(apps.refreshed) != 1 || apps.refreshed[0] != "argocd/web" {
		t.Fatalf("refreshed = %v", apps.refreshed)
	}

	apps.set("argocd/web", AppState{Sync: "Synced", Health: "Progressing", Revision: "abc", Images: []string{"reg.io/web:v2", "redis:7"}})
	r = env.tick(t, r)
	if s := stepStatus(r, "wait"); s.Status != StepRunning || s.Message != "Synced and Progressing" || s.Health != "Progressing" {
		t.Fatalf("progressing = %+v", s)
	}

	apps.set("argocd/web", AppState{Sync: "Synced", Health: "Healthy", Revision: "abc", Images: []string{"reg.io/web:v2", "redis:7"}})
	r = env.tick(t, r)
	s = stepStatus(r, "wait")
	if s.Status != StepSucceeded || s.SHA != "abc" || s.Images != "redis:7,reg.io/web:v2" || s.FinishedAt.IsZero() {
		t.Fatalf("healthy = %+v", s)
	}
	wantSteps(t, r, map[string]string{"notify": StepRunning})
	in := env.ci.lastTrigger("notify.yml").Inputs
	if in["health"] != "Healthy" || in["rev"] != "abc" || in["images"] != "redis:7,reg.io/web:v2" {
		t.Fatalf("notify inputs = %v", in)
	}
}

func TestEngineArgoSync(t *testing.T) {
	env, apps := newArgoEnv()
	wait := argoStep("wait", "apps/api", "")
	wait.NoRefresh = true
	before := env.clock.Add(-time.Minute)
	apps.set("apps/api", AppState{Sync: "Synced", Health: "Healthy", OperationPhase: "Succeeded", OperationFinishedAt: before})
	r := env.start(t, flow(Stage{Steps: []Step{wait}}), nil)
	r = env.tick(t, r)
	if s := stepStatus(r, "wait"); s.Status != StepRunning || !strings.Contains(s.Message, "starts after this step") {
		t.Fatalf("old sync = %+v", s)
	}
	if len(apps.refreshed) != 0 {
		t.Fatalf("refreshed with noRefresh: %v", apps.refreshed)
	}

	env.advance(time.Minute)
	apps.set("apps/api", AppState{Sync: "Synced", Health: "Healthy", OperationPhase: "Running"})
	r = env.tick(t, r)
	if s := stepStatus(r, "wait"); s.Status != StepRunning || s.Message != "a sync is in progress" {
		t.Fatalf("running sync = %+v", s)
	}

	apps.set("apps/api", AppState{Sync: "Synced", Health: "Healthy", OperationPhase: "Succeeded", OperationFinishedAt: env.clock})
	r = env.tick(t, r)
	wantSteps(t, r, map[string]string{"wait": StepSucceeded})
	if r.Status != RunSucceeded {
		t.Fatalf("run = %s", r.Status)
	}
}

func TestEngineArgoFailures(t *testing.T) {
	t.Run("sync failed", func(t *testing.T) {
		env, apps := newArgoEnv()
		apps.set("argocd/web", AppState{OperationPhase: "Failed", OperationFinishedAt: env.clock.Add(-time.Hour)})
		r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "web", "")}}), nil)
		r = env.tick(t, r)
		wantSteps(t, r, map[string]string{"wait": StepRunning})
		env.advance(time.Minute)
		apps.set("argocd/web", AppState{Sync: "OutOfSync", OperationPhase: "Failed", OperationMessage: "hook failed", OperationFinishedAt: env.clock})
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || s.Message != "the sync failed: hook failed" {
			t.Fatalf("wait = %+v", s)
		}
		if r.Status != RunFailed {
			t.Fatalf("run = %s", r.Status)
		}
	})
	t.Run("degraded", func(t *testing.T) {
		env, apps := newArgoEnv()
		r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "web", "v2")}}), nil)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepRunning || s.Message != "application argocd/web does not exist" {
			t.Fatalf("missing app = %+v", s)
		}
		apps.set("argocd/web", AppState{Sync: "Synced", Health: "Degraded", HealthMessage: "crash loop", Images: []string{"web:v2"}})
		r = env.tick(t, r)
		s := stepStatus(r, "wait")
		if s.Status != StepRunning || s.DegradedAt.IsZero() || s.Message != "degraded: crash loop" {
			t.Fatalf("degraded = %+v", s)
		}
		env.advance(DegradedGrace)
		r = env.tick(t, r)
		wantSteps(t, r, map[string]string{"wait": StepRunning})
		env.advance(time.Second)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || !strings.Contains(s.Message, "crash loop for more than 5m0s") {
			t.Fatalf("degraded too long = %+v", s)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		env, apps := newArgoEnv()
		apps.set("argocd/web", AppState{Sync: "OutOfSync", Health: "Healthy"})
		wait := argoStep("wait", "web", "v9")
		wait.Timeout = "10m"
		r := env.start(t, flow(Stage{Steps: []Step{wait}}), nil)
		r = env.tick(t, r)
		env.advance(11 * time.Minute)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || s.Message != "timed out after 10m0s; last state: waiting for image v9 to be deployed" {
			t.Fatalf("wait = %+v", s)
		}
	})
	t.Run("namespace not allowed", func(t *testing.T) {
		env, _ := newArgoEnv()
		r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "kube-system/web", "")}}), nil)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || !strings.Contains(s.Message, `namespace "kube-system" is not one of`) {
			t.Fatalf("wait = %+v", s)
		}
	})
	t.Run("forbidden", func(t *testing.T) {
		env := newEngineEnv(EngineConfig{Apps: forbiddenApps{}, AppNamespaces: []string{"argocd"}})
		r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "web", "")}}), nil)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || !strings.Contains(s.Message, "not allowed to read") {
			t.Fatalf("wait = %+v", s)
		}
	})
	t.Run("not configured", func(t *testing.T) {
		env := newEngineEnv(EngineConfig{})
		r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "web", "")}}), nil)
		r = env.tick(t, r)
		if s := stepStatus(r, "wait"); s.Status != StepFailed || s.Message != "Argo CD access is not configured" {
			t.Fatalf("wait = %+v", s)
		}
	})
}

func TestEngineArgoCancel(t *testing.T) {
	env, apps := newArgoEnv()
	apps.set("argocd/web", AppState{Sync: "OutOfSync"})
	r := env.start(t, flow(Stage{Steps: []Step{argoStep("wait", "web", "v2")}}), nil)
	r = env.tick(t, r)
	if _, err := env.e.Cancel(context.Background(), r.Stream, r.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	r = env.tick(t, r)
	if s := stepStatus(r, "wait"); s.Status != StepCancelled || s.Message != "the stream run was cancelled" {
		t.Fatalf("wait = %+v", s)
	}
	if r.Status != RunCancelled {
		t.Fatalf("run = %s", r.Status)
	}
	if len(env.ci.triggered()) != 0 {
		t.Fatalf("triggered = %v", env.ci.triggered())
	}
}

func TestArgoProblems(t *testing.T) {
	bad := argoStep("wait", "", "")
	bad.Connection, bad.Retries, bad.Message = "app", 1, "hi"
	pipe := step("build", "app", "build.yml")
	pipe.Image = "x"
	st := flow(Stage{Steps: []Step{pipe}}, Stage{Steps: []Step{
		bad,
		argoStep("bad-app", "Not_Valid", ""),
		argoStep("tpl", "${{ params.nope }}", "${{ steps.build.sha }}"),
		argoStep("ok", "argocd/web-${{ stream.name }}", "${{ steps.build.sha }}"),
	}})
	problems := st.Problems()
	for _, want := range []string{
		`step "wait": an argocd step has no connection`,
		`step "wait": only approval steps have a message`,
		`step "wait": an argocd step is not retried automatically`,
		`step "wait": app is required`,
		`step "bad-app": invalid application "Not_Valid"`,
		`step "tpl": app: unknown param "nope"`,
		`step "build": only argocd steps have app, image or noRefresh`,
	} {
		if !hasProblem(problems, want) {
			t.Errorf("missing problem %q in %v", want, problems)
		}
	}
	for _, p := range problems {
		if p.Step == "ok" || (p.Step == "tpl" && strings.HasPrefix(p.Message, "image")) {
			t.Errorf("unexpected problem %v", p)
		}
	}
}

func TestImageMatches(t *testing.T) {
	images := []string{"reg.io/team/web:v2", "redis:7", "ghcr.io/o/api@sha256:abc"}
	for expected, want := range map[string]bool{
		"reg.io/team/web:v2":  true,
		"team/web:v2":         true,
		"web:v2":              true,
		"v2":                  true,
		"7":                   true,
		"eb:v2":               false,
		"v3":                  false,
		"o/api@sha256:abc":    true,
		"reg.io/team/web:v20": false,
		"":                    false,
	} {
		if got := ImageMatches(expected, images); got != want {
			t.Errorf("ImageMatches(%q) = %v, want %v", expected, got, want)
		}
	}
}

func TestParseApp(t *testing.T) {
	for in, want := range map[string]string{"web": "/web", "apps/web": "apps/web", " apps/web.v2 ": "apps/web.v2"} {
		ns, name, err := ParseApp(in)
		if err != nil || ns+"/"+name != want {
			t.Errorf("ParseApp(%q) = %q %q %v", in, ns, name, err)
		}
	}
	for _, in := range []string{"", "a/b/c", "/web", "apps/", "Web"} {
		if _, _, err := ParseApp(in); err == nil {
			t.Errorf("ParseApp(%q) accepted", in)
		}
	}
}
