package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

func step(id, conn, ref string) streams.Step {
	return streams.Step{ID: id, Connection: conn, Pipeline: "1", Ref: ref}
}

func testStreams() []*streams.Stream {
	return []*streams.Stream{
		{Name: "pub", Editable: true, Spec: streams.Spec{
			Params: []streams.Param{{Name: "branch", Type: streams.ParamBranch, Connection: "shared"}},
			Stages: []streams.Stage{{Steps: []streams.Step{step("build", "shared", "${{ params.branch }}")}},
				{Steps: []streams.Step{step("deploy", "gitops", "main")}}},
		}},
		{Name: "ops-only", Editable: true, Spec: streams.Spec{
			Stages: []streams.Stage{{Steps: []streams.Step{step("build", "shared", "main"), step("ops", "secret", "main")}}},
		}},
		{Name: "git", Editable: false, Spec: streams.Spec{
			Stages: []streams.Stage{{Steps: []streams.Step{step("deploy", "gitops", "main")}}},
		}},
		{Name: "empty", Editable: true},
	}
}

func streamNames(body map[string]any) []string {
	out := []string{}
	for _, s := range body["streams"].([]any) {
		out = append(out, s.(map[string]any)["name"].(string))
	}
	return out
}

func TestStreamVisibility(t *testing.T) {
	e := newEnv(false)
	_, body := e.do(t, "GET", "/api/v1/streams", nil)
	if got := strings.Join(streamNames(body), ","); got != "git,pub" {
		t.Fatalf("alice sees %q", got)
	}
	_, body = e.do(t, "GET", "/api/v1/streams", nil, as("admin", ""))
	if got := strings.Join(streamNames(body), ","); got != "empty,git,ops-only,pub" {
		t.Fatalf("admin sees %q", got)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/ops-only", nil); code != http.StatusNotFound {
		t.Fatalf("hidden stream: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/ops-only/export", nil); code != http.StatusNotFound {
		t.Fatalf("hidden stream export: %d", code)
	}
	code, body := e.do(t, "GET", "/api/v1/streams/pub", nil)
	if code != http.StatusOK || body["editable"] != true || body["version"] == "" ||
		mustJSON(body["connections"]) != `["shared","gitops"]` || mustJSON(body["problems"]) != `[]` {
		t.Fatalf("get pub: %d %v", code, body)
	}
}

func TestOnlyAdminsManageStreams(t *testing.T) {
	e := newEnv(false)
	in := map[string]any{"name": "mine", "stages": []any{}}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/streams"},
		{"PUT", "/api/v1/streams/pub"},
		{"DELETE", "/api/v1/streams/pub"},
		{"POST", "/api/v1/streams/pub/draft"},
		{"POST", "/api/v1/streams/import"},
		{"POST", "/api/v1/streams/validate"},
	} {
		if code, _ := e.do(t, c.method, c.path, in); code != http.StatusForbidden {
			t.Errorf("%s %s by non-admin: %d", c.method, c.path, code)
		}
	}
}

func TestStreamCRUD(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	in := map[string]any{
		"name":   "new",
		"params": []any{map[string]any{"name": "env", "type": "choice", "options": []string{"dev", "prod"}}},
		"stages": []any{map[string]any{"name": "Build", "steps": []any{
			map[string]any{"id": "a", "connection": "shared", "pipeline": "1", "ref": "main", "inputs": map[string]string{"env": "${{ params.env }}"}},
			map[string]any{"id": "b", "connection": "missing", "pipeline": "1", "ref": "${{ params.nope }}"},
		}}},
	}
	code, body := e.do(t, "POST", "/api/v1/streams", in, admin)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	problems := mustJSON(body["problems"])
	if !strings.Contains(problems, `connection \"missing\" does not exist`) || !strings.Contains(problems, `unknown param \"nope\"`) {
		t.Fatalf("problems = %s", problems)
	}
	version := body["version"].(string)
	if code, _ := e.do(t, "POST", "/api/v1/streams", in, admin); code != http.StatusConflict {
		t.Fatalf("duplicate create: %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams", map[string]any{"name": "Bad Name"}, admin); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams", map[string]any{"name": "x", "editable": true}, admin); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}

	upd := map[string]any{"description": "fixed", "version": version, "stages": []any{map[string]any{"steps": []any{
		map[string]any{"id": "a", "connection": "shared", "pipeline": "1", "ref": "main"},
	}}}}
	code, body = e.do(t, "PUT", "/api/v1/streams/new", upd, admin)
	if code != http.StatusOK || body["description"] != "fixed" || mustJSON(body["problems"]) != "[]" {
		t.Fatalf("update: %d %v", code, body)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/streams/new", upd, admin); code != http.StatusConflict {
		t.Fatalf("stale update: %d", code)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/streams/new", map[string]any{"name": "other"}, admin); code != http.StatusBadRequest {
		t.Fatalf("rename: %d", code)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/streams/git", map[string]any{}, admin); code != http.StatusConflict {
		t.Fatalf("update declarative: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/streams/git", nil, admin); code != http.StatusConflict {
		t.Fatalf("delete declarative: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/v1/streams/new", nil, admin); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/streams/new", nil, admin); code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", code)
	}
}

func TestStreamDraftExportImport(t *testing.T) {
	e := newEnv(false)
	admin := as("admin", "")
	code, body := e.do(t, "POST", "/api/v1/streams/git/draft", nil, admin)
	if code != http.StatusCreated || body["name"] != "git-draft" || body["draftOf"] != "git" || body["editable"] != true {
		t.Fatalf("draft: %d %v", code, body)
	}
	// A draft of a draft still points to the original.
	code, body = e.do(t, "POST", "/api/v1/streams/git-draft/draft", map[string]string{"name": "git-v2"}, admin)
	if code != http.StatusCreated || body["draftOf"] != "git" {
		t.Fatalf("draft of draft: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/git/draft", nil, admin); code != http.StatusConflict {
		t.Fatalf("second default draft: %d", code)
	}

	// Edits keep the draft link.
	draft, _ := e.strms.Get(context.Background(), "git-draft")
	upd := map[string]any{"description": "edited", "stages": draft.Stages}
	if code, body := e.do(t, "PUT", "/api/v1/streams/git-draft", upd, admin); code != http.StatusOK || body["draftOf"] != "git" {
		t.Fatalf("update draft: %d %v", code, body)
	}

	code, body = e.do(t, "GET", "/api/v1/streams/git-draft/export", nil, admin)
	manifest, _ := body["yaml"].(string)
	if code != http.StatusOK || body["name"] != "git" || body["fileName"] != "zea-stream-git.yaml" ||
		!strings.Contains(manifest, "namespace: zea-connections") || !strings.Contains(manifest, "description: edited") {
		t.Fatalf("export: %d %v", code, body)
	}

	// Importing the export under the declarative name is refused; renaming
	// it works.
	if code, _ := e.do(t, "POST", "/api/v1/streams/import", map[string]any{"yaml": manifest, "replace": true}, admin); code != http.StatusConflict {
		t.Fatalf("import over declarative: %d", code)
	}
	renamed := strings.ReplaceAll(manifest, "name: git", "name: imported")
	code, body = e.do(t, "POST", "/api/v1/streams/import", map[string]any{"yaml": renamed}, admin)
	if code != http.StatusCreated || body["name"] != "imported" || body["description"] != "edited" || body["editable"] != true {
		t.Fatalf("import: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/import", map[string]any{"yaml": renamed}, admin); code != http.StatusConflict {
		t.Fatalf("import duplicate: %d", code)
	}
	replaced := strings.ReplaceAll(renamed, "description: edited", "description: replaced")
	code, body = e.do(t, "POST", "/api/v1/streams/import", map[string]any{"yaml": replaced, "replace": true}, admin)
	if code != http.StatusOK || body["description"] != "replaced" {
		t.Fatalf("import replace: %d %v", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/v1/streams/import", map[string]any{"yaml": "kind: Secret"}, admin); code != http.StatusBadRequest {
		t.Fatalf("import secret: %d", code)
	}
}

func TestValidateStream(t *testing.T) {
	e := newEnv(false)
	in := map[string]any{"stages": []any{map[string]any{"steps": []any{
		map[string]any{"id": "a", "connection": "nope", "pipeline": "1", "ref": "main"},
	}}}}
	code, body := e.do(t, "POST", "/api/v1/streams/validate", in, as("admin", ""))
	if code != http.StatusOK || !strings.Contains(mustJSON(body["problems"]), `connection \"nope\" does not exist`) {
		t.Fatalf("validate: %d %v", code, body)
	}
}
