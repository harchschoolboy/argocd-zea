package images

import (
	"regexp"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

// dockerHub is the canonical host of Docker Hub images.
const dockerHub = "docker.io"

// Ref is a parsed container image reference.
type Ref struct {
	// Host is the lowercased registry host; Docker Hub is "docker.io".
	Host string
	// Path is the repository path below the host.
	Path   string
	Tag    string
	Digest string
}

// ParseRef parses "[host/]path[:tag][@digest]". References without a host
// are Docker Hub images; single-segment ones get the "library/" prefix.
func ParseRef(s string) (Ref, bool) {
	s = strings.TrimSpace(s)
	var r Ref
	if i := strings.Index(s, "@"); i >= 0 {
		r.Digest = s[i+1:]
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		r.Tag = s[i+1:]
		s = s[:i]
	}
	if s == "" || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.Contains(s, "//") {
		return Ref{}, false
	}
	first, rest, found := strings.Cut(s, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host, r.Path = strings.ToLower(first), rest
	} else {
		r.Host, r.Path = dockerHub, s
	}
	r.Host = normalizeHost(r.Host)
	if r.Host == dockerHub && !strings.Contains(r.Path, "/") {
		r.Path = "library/" + r.Path
	}
	return r, true
}

func normalizeHost(h string) string {
	switch h {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return dockerHub
	}
	return h
}

// Matcher maps image references to the image sources of Connections.
type Matcher struct {
	sources []compiledSource
}

type compiledSource struct {
	connection string
	registry   string
	host       string
	namespace  string
	repoRe     *regexp.Regexp
	tagRe      *regexp.Regexp
}

// Match is an image reference that belongs to an image source of a
// Connection.
type Match struct {
	Image      string `json:"image"`
	Connection string `json:"connection"`
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitURL  string `json:"commitURL,omitempty"`
}

// Add registers the image sources of a Connection; sources with unknown
// registries or invalid patterns are ignored.
func (m *Matcher) Add(conn string, srcs []connections.ImageSource, regs map[string]*registries.Registry) {
	for _, s := range srcs {
		reg := regs[s.Registry]
		if reg == nil {
			continue
		}
		loc, err := registries.ParseURL(reg.URL)
		if err != nil {
			continue
		}
		repoRe, err := regexp.Compile(s.Repository)
		if err != nil {
			continue
		}
		var tagRe *regexp.Regexp
		if s.Tags != "" {
			if tagRe, err = regexp.Compile(s.Tags); err != nil {
				continue
			}
		}
		m.sources = append(m.sources, compiledSource{
			connection: conn,
			registry:   reg.Name,
			host:       normalizeHost(loc.Host),
			namespace:  loc.Namespace,
			repoRe:     repoRe,
			tagRe:      tagRe,
		})
	}
}

// Match returns one match per Connection whose image sources cover image.
func (m *Matcher) Match(image string) []Match {
	ref, ok := ParseRef(image)
	if !ok {
		return nil
	}
	out := []Match{}
	seen := map[string]bool{}
	for _, s := range m.sources {
		if seen[s.connection] || s.host != ref.Host {
			continue
		}
		repo := ref.Path
		if s.namespace != "" {
			var ok bool
			if repo, ok = strings.CutPrefix(ref.Path, s.namespace+"/"); !ok {
				continue
			}
		}
		rm := s.repoRe.FindStringSubmatch(repo)
		if rm == nil {
			continue
		}
		res := Match{Image: image, Connection: s.connection, Registry: s.registry, Repository: repo, Tag: ref.Tag}
		if i := s.repoRe.SubexpIndex(BranchGroup); i >= 0 {
			res.Branch = rm[i]
		}
		if s.tagRe != nil {
			tm := s.tagRe.FindStringSubmatch(ref.Tag)
			if ref.Tag == "" || tm == nil {
				continue
			}
			if i := s.tagRe.SubexpIndex(BranchGroup); i >= 0 && res.Branch == "" {
				res.Branch = tm[i]
			}
			if i := s.tagRe.SubexpIndex(SHAGroup); i >= 0 {
				res.Commit = tm[i]
			}
		}
		if res.Commit == "" && shaRe.MatchString(ref.Tag) {
			res.Commit = ref.Tag
		}
		seen[s.connection] = true
		out = append(out, res)
	}
	return out
}
