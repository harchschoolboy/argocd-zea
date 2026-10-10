// Package server wires HTTP routes and middleware for the Zea backend.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
)

// HeaderProxyToken carries the shared secret injected by Argo CD
// (extension.config.zea -> services[].headers). Argo CD overrides any
// client-supplied value with the configured one, so clients cannot spoof it.
const HeaderProxyToken = "Zea-Proxy-Token"

type ctxKey int

const (
	identityKey ctxKey = iota
	accessKey
)

// IdentityFrom returns the Argo CD identity attached by requireIdentity.
func IdentityFrom(ctx context.Context) *argocd.Identity {
	id, _ := ctx.Value(identityKey).(*argocd.Identity)
	return id
}

// requireProxyToken rejects requests that did not pass through Argo CD.
func requireProxyToken(token string, skip bool, log *slog.Logger, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !skip {
			got := []byte(r.Header.Get(HeaderProxyToken))
			if len(got) == 0 || subtle.ConstantTimeCompare(got, expected) != 1 {
				log.Warn("rejected request without valid proxy token", "path", r.URL.Path, "remote", r.RemoteAddr)
				writeError(w, http.StatusUnauthorized, "request did not come through the Argo CD proxy")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireIdentity parses Argo CD identity headers and stores them in the context.
func requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := argocd.IdentityFromRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
	})
}

// requireAnchor accepts only requests scoped to the anchor Application.
// Argo CD authorized "applications get" on exactly the Application named in
// the header, so restricting it to the anchor turns that check into the
// "may use Zea" permission.
func requireAnchor(anchor string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFrom(r.Context())
		if id == nil || id.ApplicationNamespace+":"+id.ApplicationName != anchor {
			writeError(w, http.StatusForbidden, "requests must be scoped to the Zea anchor Application "+anchor)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests logs every request with its outcome and the Argo CD user.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := slog.LevelDebug
		if rec.status >= 400 {
			level = slog.LevelWarn
		}
		log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"user", r.Header.Get(argocd.HeaderUsername),
			"app", r.Header.Get(argocd.HeaderApplicationName),
		)
	})
}

// recoverPanics turns handler panics into 500 responses.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic in handler", "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
