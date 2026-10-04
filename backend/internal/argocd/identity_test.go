package argocd

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestIdentityFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/whoami", nil)
	r.Header.Set(HeaderApplicationName, "argocd:my-app")
	r.Header.Set(HeaderProjectName, "default")
	r.Header.Set(HeaderUsername, "alice")
	r.Header.Set(HeaderUserID, "alice-sub")
	r.Header.Set(HeaderUserGroups, "devs, ops,,")
	r.Header.Set(HeaderNamespace, "argocd")

	id, err := IdentityFromRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &Identity{
		Username:             "alice",
		UserID:               "alice-sub",
		Groups:               []string{"devs", "ops"},
		ArgoCDNamespace:      "argocd",
		ApplicationNamespace: "argocd",
		ApplicationName:      "my-app",
		ProjectName:          "default",
	}
	if !reflect.DeepEqual(id, want) {
		t.Fatalf("got %+v, want %+v", id, want)
	}
}

func TestIdentityFromRequestMissingHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if _, err := IdentityFromRequest(r); !errors.Is(err, ErrMissingHeader) {
		t.Fatalf("expected ErrMissingHeader for missing app, got %v", err)
	}

	r.Header.Set(HeaderApplicationName, "argocd:my-app")
	if _, err := IdentityFromRequest(r); !errors.Is(err, ErrMissingHeader) {
		t.Fatalf("expected ErrMissingHeader for missing project, got %v", err)
	}
}

func TestParseApplicationHeader(t *testing.T) {
	cases := []struct {
		in      string
		ns, n   string
		wantErr bool
	}{
		{"argocd:app", "argocd", "app", false},
		{"app", "", "", true},
		{":app", "", "", true},
		{"argocd:", "", "", true},
		{"a:b:c", "", "", true},
	}
	for _, c := range cases {
		ns, n, err := ParseApplicationHeader(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if ns != c.ns || n != c.n {
			t.Errorf("%q: got %q/%q want %q/%q", c.in, ns, n, c.ns, c.n)
		}
	}
}
