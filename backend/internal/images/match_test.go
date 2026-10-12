package images

import (
	"testing"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                             {Host: "docker.io", Path: "library/nginx"},
		"nginx:1.27":                        {Host: "docker.io", Path: "library/nginx", Tag: "1.27"},
		"acme/api:v1":                       {Host: "docker.io", Path: "acme/api", Tag: "v1"},
		"index.docker.io/acme/api":          {Host: "docker.io", Path: "acme/api"},
		"GHCR.io/o/r:abc@sha256:00":         {Host: "ghcr.io", Path: "o/r", Tag: "abc", Digest: "sha256:00"},
		"localhost:5000/x":                  {Host: "localhost:5000", Path: "x"},
		"localhost/x:1":                     {Host: "localhost", Path: "x", Tag: "1"},
		"reg.example.com:443/a/b/c:main-1a": {Host: "reg.example.com:443", Path: "a/b/c", Tag: "main-1a"},
	} {
		got, ok := ParseRef(in)
		if !ok || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", ":tag", "a//b", "/a", "a/"} {
		if _, ok := ParseRef(in); ok {
			t.Errorf("ParseRef(%q) should fail", in)
		}
	}
}

func TestMatcher(t *testing.T) {
	regs := map[string]*registries.Registry{
		"gar": {Name: "gar", URL: "europe-docker.pkg.dev/proj/images"},
		"hub": {Name: "hub", URL: "https://registry-1.docker.io/acme"},
	}
	var m Matcher
	m.Add("api", []connections.ImageSource{
		{Registry: "gar", Repository: `^api$`, Tags: `^(?P<branch>[a-z0-9-]+)-(?P<sha>[0-9a-f]{7,40})$`},
		{Registry: "missing", Repository: `.*`},
	}, regs)
	m.Add("web", []connections.ImageSource{{Registry: "hub", Repository: `^web-(?P<branch>.+)$`}}, regs)
	m.Add("bad", []connections.ImageSource{{Registry: "gar", Repository: `(`}}, regs)

	got := m.Match("europe-docker.pkg.dev/proj/images/api:main-1a2b3c4")
	if len(got) != 1 || got[0].Connection != "api" || got[0].Branch != "main" || got[0].Commit != "1a2b3c4" || got[0].Repository != "api" {
		t.Fatalf("api: %+v", got)
	}
	if got := m.Match("europe-docker.pkg.dev/proj/images/api:latest"); len(got) != 0 {
		t.Fatalf("tag pattern must match: %+v", got)
	}
	if got := m.Match("europe-docker.pkg.dev/proj/other/api:main-1a2b3c4"); len(got) != 0 {
		t.Fatalf("namespace must match: %+v", got)
	}
	got = m.Match("acme/web-dev:0123abc")
	if len(got) != 1 || got[0].Connection != "web" || got[0].Branch != "dev" || got[0].Commit != "0123abc" {
		t.Fatalf("web: %+v", got)
	}
}
