package streams

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

func sample() *Stream {
	return &Stream{Name: "release", Spec: Spec{
		Description: "Build and deploy",
		Params: []Param{
			{Name: "branch", Type: ParamBranch, Connection: "api", Default: "main", Required: true},
			{Name: "env", Type: ParamChoice, Options: []string{"dev", "prod"}, Default: "dev"},
		},
		Stages: []Stage{
			{Name: "Build", Steps: []Step{
				{ID: "api", Connection: "api", Pipeline: "101", Ref: "${{ params.branch }}"},
				{ID: "web", Connection: "web", Pipeline: "202", Ref: "${{params.branch}}", Inputs: map[string]string{"env": "${{ params.env }}"}},
			}},
			{Name: "Deploy", Steps: []Step{
				{ID: "deploy", Connection: "deploy", Pipeline: "303", Ref: "main",
					Variables: map[string]string{"API_SHA": "${{ steps.api.sha }}", "BY": "${{ zea.user }}"}},
			}},
			{Name: "Notify", Steps: []Step{
				{ID: "notify", Connection: "deploy", Pipeline: "404", Ref: "main", Needs: []string{"api"}, When: WhenAlways,
					Inputs: map[string]string{"run": "${{ stream.run }}", "status": "${{ steps.api.status }}"}},
			}},
		},
	}}
}

func messages(ps []Problem) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return strings.Join(out, "\n")
}

func TestValidStreamHasNoProblems(t *testing.T) {
	st := sample()
	if err := st.Validate(); err != nil {
		t.Fatal(err)
	}
	if ps := st.Problems(); len(ps) > 0 {
		t.Fatalf("unexpected problems:\n%s", messages(ps))
	}
	if got := strings.Join(st.Connections(), ","); got != "api,web,deploy" {
		t.Fatalf("connections = %s", got)
	}
}

func TestGraph(t *testing.T) {
	g := sample().Graph()
	if got := strings.Join(g["api"], ","); got != "" {
		t.Fatalf("api deps = %q", got)
	}
	if got := strings.Join(g["deploy"], ","); got != "api,web" {
		t.Fatalf("deploy deps = %q", got)
	}
	if got := strings.Join(g["notify"], ","); got != "api" {
		t.Fatalf("notify deps = %q", got)
	}
	up := g.Upstream("deploy")
	if !up["api"] || !up["web"] || up["notify"] {
		t.Fatalf("upstream of deploy = %v", up)
	}
	// An empty stage does not cut the chain.
	st := &Stream{Spec: Spec{Stages: []Stage{{Steps: []Step{{ID: "a"}}}, {}, {Steps: []Step{{ID: "b"}}}}}}
	if got := strings.Join(st.Graph()["b"], ","); got != "a" {
		t.Fatalf("b deps across an empty stage = %q", got)
	}
}

func TestProblems(t *testing.T) {
	st := &Stream{Name: "bad", Spec: Spec{
		Params: []Param{
			{Name: "1x", Type: ParamString},
			{Name: "env", Type: ParamChoice},
			{Name: "env", Type: ParamString},
			{Name: "flag", Type: ParamBoolean, Default: "yes"},
			{Name: "br", Type: ParamBranch},
			{Name: "s", Type: ParamString, Options: []string{"a"}, Connection: "api"},
			{Name: "t", Type: "number"},
		},
		Stages: []Stage{
			{Steps: []Step{
				{ID: "a", Connection: "api", Pipeline: "1", Ref: "${{ params.nope }}", Needs: []string{"b"}},
				{ID: "b", Ref: " ", When: "sometimes", Timeout: "10s", Retries: 9, RetryDelay: "2h", Variables: map[string]string{"bad-name": "x"}},
				{ID: "a", Connection: "api", Pipeline: "1", Ref: "${{ steps.b.sha }}"},
			}},
			{},
			{Steps: []Step{
				{ID: "Upper", Connection: "api", Pipeline: "1", Ref: "${{ steps.a.color }}",
					Inputs: map[string]string{"x": "${{ params.env", "y": "${{ secrets.TOKEN }}", "z": "${{ }}"}},
				{ID: "self", Connection: "api", Pipeline: "1", Ref: "main", Needs: []string{"self"}},
			}},
		},
	}}
	if err := st.Validate(); err != nil {
		t.Fatalf("incomplete streams must be storable: %v", err)
	}
	got := messages(st.Problems())
	for _, want := range []string{
		`param "1x": param 1: invalid name`,
		`param "env": a choice needs options`,
		`param "env": duplicate param name`,
		`param "flag": boolean default`,
		`param "br": a branch param needs a connection`,
		`param "s": only choice params have options`,
		`param "s": only branch params have a connection`,
		`param "t": unknown type "number"`,
		`step "a": ref: unknown param "nope"`,
		`step "a": needs "b", which is not a step of an earlier stage`,
		`step "b": connection is required`,
		`step "b": pipeline is required`,
		`step "b": ref (branch or tag) is required`,
		`step "b": unknown when "sometimes"`,
		`step "b": timeout "10s" must be a duration`,
		`step "b": retries must be between 0 and 5`,
		`step "b": retry delay "2h" must be a duration up to 1h0m0s`,
		`step "b": invalid variable name "bad-name"`,
		`step "a": duplicate step id`,
		`step "a": ref: step "b" does not finish before this step`,
		"stage 2 has no steps",
		`step "Upper": invalid id`,
		`step "Upper": ref: unknown step field "color"`,
		`step "Upper": input x: unclosed "${{"`,
		`step "Upper": input y: unknown reference "secrets.TOKEN"`,
		`step "Upper": input z: empty ${{ }}`,
		`step "self": a step cannot need itself`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem %q in:\n%s", want, got)
		}
	}
	if ps := (&Stream{Name: "empty"}).Problems(); messages(ps) != "the stream has no stages" {
		t.Fatalf("empty stream: %s", messages(ps))
	}
}

func TestValidateLimits(t *testing.T) {
	cases := map[string]*Stream{
		"name":   {Name: "Bad_Name"},
		"draft":  {Name: "ok", DraftOf: "NO"},
		"stages": {Name: "ok", Spec: Spec{Stages: make([]Stage, MaxStages+1)}},
		"params": {Name: "ok", Spec: Spec{Params: make([]Param, MaxParams+1)}},
		"steps":  {Name: "ok", Spec: Spec{Stages: []Stage{{Steps: make([]Step, MaxStepsPerStage+1)}}}},
		"value":  {Name: "ok", Spec: Spec{Stages: []Stage{{Steps: []Step{{ID: "a", Inputs: map[string]string{"k": strings.Repeat("x", maxValueLen+1)}}}}}}},
	}
	total := &Stream{Name: "ok"}
	for i := 0; i < 3; i++ {
		total.Stages = append(total.Stages, Stage{Steps: make([]Step, MaxStepsPerStage)})
	}
	cases["total"] = total
	for name, st := range cases {
		if err := st.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRender(t *testing.T) {
	vals := map[string]string{"params.branch": "feat/x", "steps.api.sha": "abc", "zea.user": "alice"}
	got, err := Render("${{ params.branch }}@${{steps.api.sha}} by ${{ zea.user }} $ {{ literal }}", vals)
	if err != nil || got != "feat/x@abc by alice $ {{ literal }}" {
		t.Fatalf("render = %q, %v", got, err)
	}
	if _, err := Render("${{ params.missing }}", vals); err == nil {
		t.Fatal("expected an error for a missing value")
	}
	if _, err := Render("${{ params.branch", vals); err == nil {
		t.Fatal("expected an error for an unclosed reference")
	}
}

func TestSpecRoundTrip(t *testing.T) {
	st := sample()
	raw, err := FormatSpec(st.Spec)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := FormatSpec(back)
	if again != raw {
		t.Fatalf("round trip changed the spec:\n%s\n---\n%s", raw, again)
	}
	if _, err := ParseSpec("stages:\n  - steps:\n      - id: a\n        typo: 1\n"); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}

func TestExportImport(t *testing.T) {
	st := sample()
	st.Name = "release-draft"
	st.DraftOf = "release"
	name, manifest, err := Export(st, "zea-connections")
	if err != nil {
		t.Fatal(err)
	}
	if name != "release" {
		t.Fatalf("a draft must be exported under its original name, got %q", name)
	}
	for _, want := range []string{"kind: ConfigMap", "name: zea-stream-release", "namespace: zea-connections", "argocd-zea.io/type: stream", "stream.yaml: |"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest lacks %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, connections.LabelManagedBy) || strings.Contains(manifest, "draftOf") {
		t.Fatalf("exported manifest must be declarative:\n%s", manifest)
	}

	imported, err := Import(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Name != "release" || imported.DraftOf != "" || len(imported.Stages) != 3 || imported.Stages[1].Steps[0].Variables["API_SHA"] != "${{ steps.api.sha }}" {
		t.Fatalf("imported = %+v", imported)
	}

	plain := "name: tiny\ndescription: d\nstages:\n  - steps:\n      - id: a\n        connection: api\n        pipeline: \"1\"\n        ref: main\n"
	imported, err = Import(plain)
	if err != nil || imported.Name != "tiny" || imported.Stages[0].Steps[0].Pipeline != "1" {
		t.Fatalf("plain import = %+v, %v", imported, err)
	}

	for name, raw := range map[string]string{
		"empty":     "  ",
		"secret":    "kind: Secret\nmetadata:\n  name: x\n",
		"no spec":   "kind: ConfigMap\nmetadata:\n  name: zea-stream-x\ndata:\n  name: x\n",
		"bad spec":  "kind: ConfigMap\nmetadata:\n  name: zea-stream-x\ndata:\n  stream.yaml: \"stages: [{typo: 1}]\"\n",
		"no name":   "stages: []\n",
		"unknown":   "name: x\nfoo: bar\n",
		"not yaml":  "name: [",
		"too large": "name: x\ndescription: " + strings.Repeat("x", maxImportBytes) + "\n",
	} {
		if _, err := Import(raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestConfigMapStore(t *testing.T) {
	ctx := context.Background()
	ns := "zea-connections"
	spec, _ := FormatSpec(sample().Spec)
	git := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "zea-stream-nightly", Namespace: ns,
			Labels:      map[string]string{LabelType: TypeStream, connections.LabelManagedBy: connections.ManagedByZea},
			Annotations: map[string]string{annotationArgoTracking: "zea:/ConfigMap:zea-connections/zea-stream-nightly"}},
		Data: map[string]string{KeyName: "nightly", KeySpec: spec},
	}
	broken := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "zea-stream-broken", Namespace: ns, Labels: map[string]string{LabelType: TypeStream}},
		Data:       map[string]string{KeySpec: "stages: {"},
	}
	unrelated := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns}}
	s := NewConfigMapStore(fake.NewClientset(git, broken, unrelated), ns)

	all, err := s.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %v, %v", all, err)
	}
	if all[0].Name != "broken" || all[0].ParseError == "" || all[0].Editable {
		t.Fatalf("broken = %+v", all[0])
	}
	if all[1].Name != "nightly" || all[1].Editable || len(all[1].Stages) != 3 {
		t.Fatalf("an Argo CD tracked stream must be read-only: %+v", all[1])
	}
	if err := s.Delete(ctx, "nightly"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete declarative: %v", err)
	}
	if _, err := s.Update(ctx, &Stream{Name: "nightly"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("update declarative: %v", err)
	}

	st := sample()
	created, err := s.Create(ctx, st)
	if err != nil || !created.Editable || created.ConfigMapName != "zea-stream-release" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := s.Create(ctx, st); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	cm, _ := s.client.CoreV1().ConfigMaps(ns).Get(ctx, "zea-stream-release", metav1.GetOptions{})
	if cm.Labels[LabelType] != TypeStream || cm.Labels[connections.LabelManagedBy] != connections.ManagedByZea || !strings.Contains(cm.Data[KeySpec], "steps.api.sha") {
		t.Fatalf("stored configmap = %+v", cm)
	}

	// The fake client does not bump resourceVersion, so set one by hand to
	// check that a stale version is rejected.
	cm.ResourceVersion = "7"
	if _, err := s.client.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	upd := sample()
	upd.Description = "changed"
	upd.ResourceVersion = "6"
	if _, err := s.Update(ctx, upd); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	upd.ResourceVersion = "7"
	upd.DraftOf = "nightly"
	updated, err := s.Update(ctx, upd)
	if err != nil || updated.Description != "changed" || updated.DraftOf != "nightly" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if err := s.Delete(ctx, "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "release"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestMemoryStoreCopies(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	st := sample()
	if _, err := m.Create(ctx, st); err != nil {
		t.Fatal(err)
	}
	st.Stages[0].Steps[0].Ref = "mutated"
	got, _ := m.Get(ctx, "release")
	if got.Stages[0].Steps[0].Ref == "mutated" {
		t.Fatal("the store must keep its own copy")
	}
	got.ResourceVersion = "999"
	if _, err := m.Update(ctx, got); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
}
