// Package images resolves a Connection's image sources into the matching
// repositories and tags of its registries.
package images

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

const (
	// MaxRepositories caps the repositories returned for one request.
	MaxRepositories = 30
	// DefaultTagLimit is the number of tags returned per repository.
	DefaultTagLimit = 10
	// MaxTagLimit caps the requested tags per repository.
	MaxTagLimit = 100
	// maxDescribe caps per-repository manifest lookups for registries whose
	// tag lists carry no metadata.
	maxDescribe     = 20
	repoWorkers     = 4
	maxCacheEntries = 2000
)

// BranchGroup is the named regex group that ties an image to a branch.
const BranchGroup = "branch"

// SHAGroup is the named regex group in a tag pattern holding a commit SHA.
const SHAGroup = "sha"

var (
	slugRe = regexp.MustCompile(`[^a-z0-9._-]`)
	shaRe  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// Slug turns a branch name into the form used in image names: lowercase,
// with every character other than a-z, 0-9, ".", "_" and "-" replaced by "-".
func Slug(ref string) string {
	return slugRe.ReplaceAllString(strings.ToLower(ref), "-")
}

// Options control one resolution.
type Options struct {
	// Ref filters images by the "branch" group when set.
	Ref string
	// TagLimit is the number of newest tags returned per repository.
	TagLimit int
	// Refresh bypasses the listing cache.
	Refresh bool
	// CommitURL builds a link to a commit; nil disables commit links.
	CommitURL func(sha string) string
}

// Result is the response of Resolve.
type Result struct {
	Repositories []Repository  `json:"repositories"`
	Errors       []SourceError `json:"errors,omitempty"`
	// Branch is the slug used for filtering, empty when not filtered.
	Branch string `json:"branch,omitempty"`
	// Truncated is set when more than MaxRepositories matched.
	Truncated bool `json:"truncated,omitempty"`
}

// Repository is a matched image repository with its newest tags.
type Repository struct {
	Registry string `json:"registry"`
	Name     string `json:"name"`
	// Image is the pullable reference without a tag.
	Image    string `json:"image"`
	Branch   string `json:"branch,omitempty"`
	TagCount int    `json:"tagCount"`
	Tags     []Tag  `json:"tags"`
	Error    string `json:"error,omitempty"`
	updated  time.Time
}

// Tag is a matched tag.
type Tag struct {
	registries.Tag
	// Image is the full pullable reference including the tag.
	Image     string `json:"image"`
	Commit    string `json:"commit,omitempty"`
	CommitURL string `json:"commitURL,omitempty"`
}

// SourceError reports an image source that could not be resolved.
type SourceError struct {
	// Source is the 1-based index of the image source.
	Source   int    `json:"source"`
	Registry string `json:"registry"`
	Error    string `json:"error"`
}

// Resolver lists images with a short-lived cache of registry listings.
type Resolver struct {
	registries registries.Store
	kinds      *registries.Kinds
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value   any
	expires time.Time
}

// NewResolver returns a Resolver caching registry listings for ttl.
func NewResolver(store registries.Store, kinds *registries.Kinds, ttl time.Duration) *Resolver {
	return &Resolver{registries: store, kinds: kinds, ttl: ttl, cache: map[string]cacheEntry{}}
}

type job struct {
	reg    *registries.Registry
	client registries.Client
	loc    *registries.Location
	repo   registries.Repository
	branch string
	tagRe  *regexp.Regexp
}

// Resolve matches srcs against the registries and returns the newest tags.
// Errors of single sources or repositories are reported in the result.
func (rv *Resolver) Resolve(ctx context.Context, srcs []connections.ImageSource, opts Options) *Result {
	res := &Result{Repositories: []Repository{}}
	slug := ""
	if opts.Ref != "" {
		slug = Slug(opts.Ref)
		res.Branch = slug
	}
	if opts.TagLimit <= 0 {
		opts.TagLimit = DefaultTagLimit
	}
	if opts.TagLimit > MaxTagLimit {
		opts.TagLimit = MaxTagLimit
	}

	jobs := []job{}
	seen := map[string]bool{}
	for i, s := range srcs {
		fail := func(err error) {
			res.Errors = append(res.Errors, SourceError{Source: i + 1, Registry: s.Registry, Error: err.Error()})
		}
		repoRe, err := regexp.Compile(s.Repository)
		if err != nil {
			fail(err)
			continue
		}
		var tagRe *regexp.Regexp
		if s.Tags != "" {
			if tagRe, err = regexp.Compile(s.Tags); err != nil {
				fail(err)
				continue
			}
		}
		reg, err := rv.registries.Get(ctx, s.Registry)
		if err != nil {
			fail(err)
			continue
		}
		client, err := rv.kinds.Get(reg.Kind)
		if err != nil {
			fail(err)
			continue
		}
		loc, err := registries.ParseURL(reg.URL)
		if err != nil {
			fail(err)
			continue
		}
		repos, err := rv.repositories(ctx, reg, client, opts.Refresh)
		if err != nil {
			fail(err)
			continue
		}
		bi := repoRe.SubexpIndex(BranchGroup)
		for _, repo := range repos {
			m := repoRe.FindStringSubmatch(repo.Name)
			if m == nil {
				continue
			}
			branch := ""
			if bi >= 0 {
				branch = m[bi]
				if slug != "" && branch != slug {
					continue
				}
			}
			key := reg.Name + "/" + repo.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(jobs) >= MaxRepositories {
				res.Truncated = true
				continue
			}
			jobs = append(jobs, job{reg: reg, client: client, loc: loc, repo: repo, branch: branch, tagRe: tagRe})
		}
	}

	out := make([]Repository, len(jobs))
	keep := make([]bool, len(jobs))
	sem := make(chan struct{}, repoWorkers)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i], keep[i] = rv.repository(ctx, jobs[i], slug, opts)
		}(i)
	}
	wg.Wait()
	for i := range out {
		if keep[i] {
			res.Repositories = append(res.Repositories, out[i])
		}
	}
	sort.SliceStable(res.Repositories, func(i, j int) bool {
		a, b := res.Repositories[i], res.Repositories[j]
		if !a.updated.Equal(b.updated) {
			return a.updated.After(b.updated)
		}
		return a.Registry+"/"+a.Name < b.Registry+"/"+b.Name
	})
	return res
}

type matchedTag struct {
	tag    registries.Tag
	commit string
}

// repository lists and filters the tags of one repository. It returns false
// when a branch filter on tags leaves nothing to show.
func (rv *Resolver) repository(ctx context.Context, j job, slug string, opts Options) (Repository, bool) {
	r := Repository{
		Registry: j.reg.Name,
		Name:     j.repo.Name,
		Image:    j.loc.ImageRef(j.repo.Name),
		Branch:   j.branch,
		Tags:     []Tag{},
		updated:  j.repo.UpdatedAt,
	}
	tags, err := rv.tags(ctx, j.reg, j.client, j.repo.Name, opts.Refresh)
	if err != nil {
		r.Error = err.Error()
		return r, true
	}
	bi, si := -1, -1
	if j.tagRe != nil {
		bi, si = j.tagRe.SubexpIndex(BranchGroup), j.tagRe.SubexpIndex(SHAGroup)
	}
	matched := []matchedTag{}
	for _, t := range tags {
		commit := ""
		if j.tagRe != nil {
			m := j.tagRe.FindStringSubmatch(t.Name)
			if m == nil {
				continue
			}
			if bi >= 0 {
				if slug != "" && m[bi] != slug {
					continue
				}
				if r.Branch == "" {
					r.Branch = m[bi]
				}
			}
			if si >= 0 {
				commit = m[si]
			}
		}
		if commit == "" && shaRe.MatchString(t.Name) {
			commit = t.Name
		}
		matched = append(matched, matchedTag{tag: t, commit: commit})
	}
	if bi >= 0 && slug != "" && len(matched) == 0 {
		return r, false
	}
	if d, ok := j.client.(registries.TagDescriber); ok && len(matched) > 0 {
		// Tag lists without metadata come in registry order; describe the
		// last ones, which are the newest for incrementing tags.
		start := max(0, len(matched)-maxDescribe)
		subset := make([]registries.Tag, 0, len(matched)-start)
		for _, m := range matched[start:] {
			subset = append(subset, m.tag)
		}
		for k, t := range d.DescribeTags(ctx, j.reg, j.repo.Name, subset) {
			matched[start+k].tag = t
		}
	}
	sort.SliceStable(matched, func(a, b int) bool {
		ta, tb := matched[a].tag, matched[b].tag
		if !ta.PushedAt.Equal(tb.PushedAt) {
			return ta.PushedAt.After(tb.PushedAt)
		}
		return ta.Name > tb.Name
	})
	r.TagCount = len(matched)
	if len(matched) > 0 && matched[0].tag.PushedAt.After(r.updated) {
		r.updated = matched[0].tag.PushedAt
	}
	for _, m := range matched[:min(len(matched), opts.TagLimit)] {
		t := Tag{Tag: m.tag, Image: r.Image + ":" + m.tag.Name, Commit: m.commit}
		if m.commit != "" && opts.CommitURL != nil {
			t.CommitURL = opts.CommitURL(m.commit)
		}
		r.Tags = append(r.Tags, t)
	}
	return r, true
}

func cacheKey(reg *registries.Registry, parts ...string) string {
	return reg.Name + "\x00" + reg.Kind + "\x00" + reg.URL + "\x00" + strings.Join(parts, "\x00")
}

func (rv *Resolver) repositories(ctx context.Context, reg *registries.Registry, c registries.Client, refresh bool) ([]registries.Repository, error) {
	key := cacheKey(reg, "repos")
	if v, ok := rv.get(key, refresh); ok {
		return v.([]registries.Repository), nil
	}
	repos, err := c.ListRepositories(ctx, reg)
	if err != nil {
		return nil, err
	}
	rv.put(key, repos)
	return repos, nil
}

func (rv *Resolver) tags(ctx context.Context, reg *registries.Registry, c registries.Client, repo string, refresh bool) ([]registries.Tag, error) {
	key := cacheKey(reg, "tags", repo)
	if v, ok := rv.get(key, refresh); ok {
		return v.([]registries.Tag), nil
	}
	tags, err := c.ListTags(ctx, reg, repo)
	if err != nil {
		return nil, err
	}
	rv.put(key, tags)
	return tags, nil
}

func (rv *Resolver) get(key string, refresh bool) (any, bool) {
	if refresh {
		return nil, false
	}
	rv.mu.Lock()
	defer rv.mu.Unlock()
	e, ok := rv.cache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (rv *Resolver) put(key string, v any) {
	if rv.ttl <= 0 {
		return
	}
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if len(rv.cache) >= maxCacheEntries {
		now := time.Now()
		for k, e := range rv.cache {
			if now.After(e.expires) {
				delete(rv.cache, k)
			}
		}
		if len(rv.cache) >= maxCacheEntries {
			rv.cache = map[string]cacheEntry{}
		}
	}
	rv.cache[key] = cacheEntry{value: v, expires: time.Now().Add(rv.ttl)}
}

// Invalidate drops cached listings of the named registry.
func (rv *Resolver) Invalidate(name string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	prefix := name + "\x00"
	for k := range rv.cache {
		if strings.HasPrefix(k, prefix) {
			delete(rv.cache, k)
		}
	}
}
