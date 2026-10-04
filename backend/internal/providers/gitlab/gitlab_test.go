package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

const projectPath = "/api/v4/projects/grp%2Fsub%2Fproj"

func fakeGitLab(t *testing.T, ciFileExists bool, prefix string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
			return
		}
		uri := strings.TrimPrefix(r.RequestURI, prefix)
		path, query, _ := strings.Cut(uri, "?")
		switch {
		case r.Method == http.MethodGet && path == projectPath:
			fmt.Fprint(w, `{"path_with_namespace":"grp/sub/proj","default_branch":"main","web_url":"https://x","ci_config_path":""}`)
		case r.Method == http.MethodGet && path == projectPath+"/repository/branches":
			if strings.Contains(query, "page=2") {
				fmt.Fprint(w, `[{"name":"dev","commit":{"id":"b"}}]`)
				return
			}
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"name":"main","protected":true,"commit":{"id":"a"}}]`)
		case r.Method == http.MethodHead && path == projectPath+"/repository/files/.gitlab-ci.yml":
			if !strings.Contains(query, "ref=feature%2Fx") {
				t.Errorf("files query = %q", query)
			}
			if !ciFileExists {
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGitLabFlow(t *testing.T) {
	srv := fakeGitLab(t, true, "")
	defer srv.Close()
	p := New(srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: srv.URL + "/grp/sub/proj.git", Credentials: map[string]string{KeyToken: "glpat"}}
	ctx := context.Background()

	if err := p.Validate(c); err != nil {
		t.Fatalf("validate: %v", err)
	}
	repo, err := p.Test(ctx, c)
	if err != nil || repo.FullName != "grp/sub/proj" {
		t.Fatalf("test: %+v %v", repo, err)
	}
	bl, err := p.ListBranches(ctx, c)
	if err != nil || len(bl.Branches) != 2 || bl.DefaultBranch != "main" || !bl.Branches[0].Protected {
		t.Fatalf("branches: %+v %v", bl, err)
	}
	pls, err := p.ListPipelines(ctx, c, "feature/x")
	if err != nil || len(pls) != 1 || !pls[0].Dispatchable || pls[0].Path != ".gitlab-ci.yml" {
		t.Fatalf("pipelines: %+v %v", pls, err)
	}
}

func TestGitLabMissingCIFile(t *testing.T) {
	srv := fakeGitLab(t, false, "")
	defer srv.Close()
	p := New(srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: srv.URL + "/grp/sub/proj", Credentials: map[string]string{KeyToken: "glpat"}}
	pls, err := p.ListPipelines(context.Background(), c, "feature/x")
	if err != nil || pls[0].Dispatchable || !strings.Contains(pls[0].Reason, "does not exist on feature/x") {
		t.Fatalf("pipelines: %+v %v", pls, err)
	}
}

func TestGitLabSubPathInstall(t *testing.T) {
	srv := fakeGitLab(t, true, "/gitlab")
	defer srv.Close()
	p := New(srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: srv.URL + "/gitlab/grp/sub/proj", APIURL: srv.URL + "/gitlab/api/v4",
		Credentials: map[string]string{KeyToken: "glpat"}}
	if _, err := p.Test(context.Background(), c); err != nil {
		t.Fatalf("test: %v", err)
	}
}

func TestGitLabBadToken(t *testing.T) {
	srv := fakeGitLab(t, true, "")
	defer srv.Close()
	p := New(srv.Client())
	c := &connections.Connection{Name: "c", Provider: ID, URL: srv.URL + "/grp/sub/proj", Credentials: map[string]string{KeyToken: "nope"}}
	if _, err := p.Test(context.Background(), c); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitLabValidate(t *testing.T) {
	p := New(http.DefaultClient)
	for _, c := range []*connections.Connection{
		{URL: "https://gitlab.com/proj", Credentials: map[string]string{KeyToken: "t"}},
		{URL: "https://gitlab.com/g/p"},
	} {
		if p.Validate(c) == nil {
			t.Errorf("%+v should fail", c)
		}
	}
}
