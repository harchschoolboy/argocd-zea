package images

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

type fakeClient struct {
	repos     []registries.Repository
	tags      map[string][]registries.Tag
	repoCalls int
	describe  bool
}

func (f *fakeClient) Info() registries.KindInfo { return registries.KindInfo{ID: "fake"} }

func (f *fakeClient) Validate(*registries.Registry) error { return nil }

func (f *fakeClient) ListRepositories(context.Context, *registries.Registry) ([]registries.Repository, error) {
	f.repoCalls++
	return f.repos, nil
}

func (f *fakeClient) ListTags(_ context.Context, _ *registries.Registry, repo string) ([]registries.Tag, error) {
	if repo == "broken" {
		return nil, errors.New("boom")
	}
	return f.tags[repo], nil
}

// describingClient adds TagDescriber to fakeClient.
type describingClient struct{ *fakeClient }

func (d describingClient) DescribeTags(_ context.Context, _ *registries.Registry, _ string, tags []registries.Tag) []registries.Tag {
	out := append([]registries.Tag(nil), tags...)
	for i := range out {
		out[i].Digest = "sha256:" + out[i].Name
		out[i].PushedAt = time.Date(2026, 1, 1, 0, 0, len(out[i].Name), 0, time.UTC)
	}
	return out
}

func day(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }

func newResolver(c registries.Client) *Resolver {
	store := registries.NewMemoryStore(&registries.Registry{Name: "do", Kind: "fake", URL: "registry.digitalocean.com/vmist"})
	return NewResolver(store, registries.NewKinds(c), time.Minute)
}

func vmistClient() *fakeClient {
	return &fakeClient{
		repos: []registries.Repository{
			{Name: "vmist-server-web-master-prod"},
			{Name: "vmist-server-web-feature-x-prod"},
			{Name: "vmist-server-static-master-prod"},
			{Name: "unrelated"},
		},
		tags: map[string][]registries.Tag{
			"vmist-server-web-master-prod": {
				{Name: "latest", PushedAt: day(5)},
				{Name: "abc1234", PushedAt: day(5)},
				{Name: "def5678", PushedAt: day(3)},
			},
			"vmist-server-web-feature-x-prod": {{Name: "0123abc", PushedAt: day(9)}},
			"vmist-server-static-master-prod": {{Name: "fff0000", PushedAt: day(1)}},
		},
	}
}

func TestResolveBranchAndTags(t *testing.T) {
	c := vmistClient()
	rv := newResolver(c)
	srcs := []connections.ImageSource{{Registry: "do", Repository: `^vmist-server-(web|static)-(?P<branch>.+)-prod$`, Tags: `^[0-9a-f]{7}$`}}

	res := rv.Resolve(context.Background(), srcs, Options{Ref: "master", CommitURL: func(s string) string { return "https://gh/c/" + s }})
	if len(res.Errors) != 0 || res.Branch != "master" || len(res.Repositories) != 2 {
		t.Fatalf("result = %+v", res)
	}
	web := res.Repositories[0]
	if web.Name != "vmist-server-web-master-prod" || web.Branch != "master" || web.TagCount != 2 ||
		web.Image != "registry.digitalocean.com/vmist/vmist-server-web-master-prod" {
		t.Fatalf("web = %+v", web)
	}
	if web.Tags[0].Name != "abc1234" || web.Tags[0].Commit != "abc1234" || web.Tags[0].CommitURL != "https://gh/c/abc1234" ||
		web.Tags[0].Image != web.Image+":abc1234" {
		t.Fatalf("newest tag = %+v", web.Tags[0])
	}

	all := rv.Resolve(context.Background(), srcs, Options{})
	if len(all.Repositories) != 3 || all.Repositories[0].Name != "vmist-server-web-feature-x-prod" {
		t.Fatalf("all branches = %+v", all.Repositories)
	}
	if c.repoCalls != 1 {
		t.Fatalf("repositories should be cached, calls = %d", c.repoCalls)
	}
	rv.Resolve(context.Background(), srcs, Options{Refresh: true})
	if c.repoCalls != 2 {
		t.Fatalf("refresh should bypass the cache, calls = %d", c.repoCalls)
	}
	rv.Invalidate("do")
	rv.Resolve(context.Background(), srcs, Options{})
	if c.repoCalls != 3 {
		t.Fatalf("invalidate should drop the cache, calls = %d", c.repoCalls)
	}

	// Slashes in branch names become "-" as in the build workflows.
	slash := rv.Resolve(context.Background(), srcs, Options{Ref: "Feature/X"})
	if len(slash.Repositories) != 1 || slash.Repositories[0].Branch != "feature-x" {
		t.Fatalf("slug = %+v", slash)
	}
}

func TestResolveBranchInTag(t *testing.T) {
	c := &fakeClient{
		repos: []registries.Repository{{Name: "app"}},
		tags: map[string][]registries.Tag{"app": {
			{Name: "main-1111111", PushedAt: day(2)},
			{Name: "dev-2222222", PushedAt: day(3)},
		}},
	}
	rv := newResolver(c)
	srcs := []connections.ImageSource{{Registry: "do", Repository: `^app$`, Tags: `^(?P<branch>.+)-(?P<sha>[0-9a-f]{7})$`}}
	res := rv.Resolve(context.Background(), srcs, Options{Ref: "main"})
	if len(res.Repositories) != 1 || res.Repositories[0].TagCount != 1 || res.Repositories[0].Tags[0].Commit != "1111111" {
		t.Fatalf("tag branch = %+v", res.Repositories)
	}
	if none := rv.Resolve(context.Background(), srcs, Options{Ref: "other"}); len(none.Repositories) != 0 {
		t.Fatalf("no tags for branch should hide the repo: %+v", none.Repositories)
	}
}

func TestResolveErrorsAndLimits(t *testing.T) {
	c := &fakeClient{repos: []registries.Repository{{Name: "broken"}}}
	for i := 0; i < MaxRepositories+5; i++ {
		c.repos = append(c.repos, registries.Repository{Name: "r" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	rv := newResolver(c)
	res := rv.Resolve(context.Background(), []connections.ImageSource{
		{Registry: "missing", Repository: ".*"},
		{Registry: "do", Repository: ".*"},
	}, Options{})
	if len(res.Errors) != 1 || res.Errors[0].Source != 1 || !res.Truncated || len(res.Repositories) != MaxRepositories {
		t.Fatalf("errors=%+v truncated=%v repos=%d", res.Errors, res.Truncated, len(res.Repositories))
	}
	found := false
	for _, r := range res.Repositories {
		if r.Name == "broken" && r.Error == "boom" {
			found = true
		}
	}
	if !found {
		t.Fatal("repository errors should be reported per repository")
	}
}

func TestResolveDescribes(t *testing.T) {
	c := &fakeClient{repos: []registries.Repository{{Name: "app"}}, tags: map[string][]registries.Tag{"app": {{Name: "a"}, {Name: "bbb"}, {Name: "cc"}}}}
	rv := newResolver(describingClient{c})
	res := rv.Resolve(context.Background(), []connections.ImageSource{{Registry: "do", Repository: "app"}}, Options{TagLimit: 2})
	tags := res.Repositories[0].Tags
	if len(tags) != 2 || tags[0].Name != "bbb" || tags[0].Digest != "sha256:bbb" || res.Repositories[0].TagCount != 3 {
		t.Fatalf("described = %+v", tags)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"master": "master", "Feature/ABC_1.2": "feature-abc_1.2", "a b+c": "a-b-c"} {
		if got := Slug(in); got != want {
			t.Fatalf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
