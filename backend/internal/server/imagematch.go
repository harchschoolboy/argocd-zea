package server

import (
	"net/http"

	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/images"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

const (
	// maxMatchImages caps the image references of one match request.
	maxMatchImages = 1000
	maxImageRefLen = 512
)

type matchInput struct {
	Images []string `json:"images"`
}

// handleMatchImages maps image references (as reported by Argo CD
// Applications) to the Connections the user may view whose image sources
// cover them. Only names and patterns are compared; no registry is called.
func (a *api) handleMatchImages(w http.ResponseWriter, r *http.Request) {
	var in matchInput
	if !decodeJSONMax(w, r, &in, 1<<20) {
		return
	}
	if len(in.Images) > maxMatchImages {
		writeError(w, http.StatusBadRequest, "too many images")
		return
	}
	for _, img := range in.Images {
		if len(img) > maxImageRefLen {
			writeError(w, http.StatusBadRequest, "image reference too long")
			return
		}
	}
	matches := []images.Match{}
	if a.deps.Registries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
		return
	}
	conns, err := a.deps.Store.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	regList, err := a.deps.Registries.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	regs := map[string]*registries.Registry{}
	for _, reg := range regList {
		regs[reg.Name] = reg
	}
	acc := AccessFrom(r.Context())
	var m images.Matcher
	linkers := map[string]func(string) string{}
	for _, c := range conns {
		if len(c.Images) == 0 || !acc.Can(authz.ResourceConnections, authz.ActionView, c.Name) {
			continue
		}
		m.Add(c.Name, c.Images, regs)
		if p, err := a.deps.Providers.Get(c.Provider); err == nil {
			if cl, ok := p.(providers.CommitLinker); ok {
				linkers[c.Name] = func(sha string) string { return cl.CommitURL(c, sha) }
			}
		}
	}
	seen := map[string]bool{}
	for _, img := range in.Images {
		if seen[img] {
			continue
		}
		seen[img] = true
		for _, mt := range m.Match(img) {
			if mt.Commit != "" && linkers[mt.Connection] != nil {
				mt.CommitURL = linkers[mt.Connection](mt.Commit)
			}
			matches = append(matches, mt)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
}
