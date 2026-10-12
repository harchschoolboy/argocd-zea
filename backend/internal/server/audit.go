package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/audit"
)

const auditNoteKey ctxKey = 100

// auditActions names the audited routes. Requests that change nothing
// (validation, previews, matching) are not audited.
var auditActions = map[string]string{
	"PUT /api/v1/policy":                                          "policy.save",
	"POST /api/v1/connections":                                    "connection.create",
	"PUT /api/v1/connections/{name}":                              "connection.update",
	"DELETE /api/v1/connections/{name}":                           "connection.delete",
	"POST /api/v1/connections/{name}/test":                        "connection.test",
	"POST /api/v1/test-connection":                                "connection.test-draft",
	"POST /api/v1/connections/{name}/runs":                        "connection.run.start",
	"POST /api/v1/connections/{name}/runs/{run}/cancel":           "connection.run.cancel",
	"POST /api/v1/connections/{name}/runs/{run}/retry":            "connection.run.retry",
	"POST /api/v1/registries":                                     "registry.create",
	"PUT /api/v1/registries/{name}":                               "registry.update",
	"DELETE /api/v1/registries/{name}":                            "registry.delete",
	"POST /api/v1/registries/{name}/test":                         "registry.test",
	"POST /api/v1/test-registry":                                  "registry.test-draft",
	"POST /api/v1/streams":                                        "stream.create",
	"POST /api/v1/streams/import":                                 "stream.import",
	"PUT /api/v1/streams/{name}":                                  "stream.update",
	"DELETE /api/v1/streams/{name}":                               "stream.delete",
	"POST /api/v1/streams/{name}/draft":                           "stream.draft",
	"POST /api/v1/streams/{name}/runs":                            "stream.run.start",
	"POST /api/v1/streams/{name}/runs/{run}/cancel":               "stream.run.cancel",
	"POST /api/v1/streams/{name}/runs/{run}/retry":                "stream.run.retry",
	"POST /api/v1/streams/{name}/runs/{run}/steps/{step}/approve": "stream.step.approve",
	"POST /api/v1/streams/{name}/runs/{run}/steps/{step}/reject":  "stream.step.reject",
}

// auditNote lets handlers name what they acted on when the path does not.
type auditNote struct {
	name   string
	target string
}

func noteAudit(ctx context.Context, name, target string) {
	if n, ok := ctx.Value(auditNoteKey).(*auditNote); ok {
		if name != "" {
			n.name = name
		}
		if target != "" {
			n.target = target
		}
	}
}

// auditWriter keeps the status and the start of error bodies.
type auditWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *auditWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && w.body.Len() < 1024 {
		w.body.Write(b[:min(len(b), 1024-w.body.Len())])
	}
	return w.ResponseWriter.Write(b)
}

// audited records the audited routes after they ran.
func (a *api) audited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || a.deps.Audit == nil {
			next.ServeHTTP(w, r)
			return
		}
		note := &auditNote{}
		r = r.WithContext(context.WithValue(r.Context(), auditNoteKey, note))
		aw := &auditWriter{ResponseWriter: w}
		next.ServeHTTP(aw, r)
		action, ok := auditActions[r.Pattern]
		if !ok {
			return
		}
		e := audit.Event{
			Action:   action,
			Resource: strings.SplitN(action, ".", 2)[0],
			Name:     r.PathValue("name"),
			Target:   r.PathValue("run"),
			Status:   aw.status,
		}
		if e.Status == 0 {
			e.Status = http.StatusOK
		}
		if step := r.PathValue("step"); step != "" {
			e.Target += "/" + step
		}
		if note.name != "" {
			e.Name = note.name
		}
		if note.target != "" {
			e.Target = note.target
		}
		if id := IdentityFrom(r.Context()); id != nil {
			e.User = id.Username
			if e.User == "" {
				e.User = id.UserID
			}
		}
		if e.Status >= 400 {
			var body errorBody
			if json.Unmarshal(aw.body.Bytes(), &body) == nil {
				e.Error = body.Error
			}
		}
		a.deps.Audit.Record(e)
	})
}

type auditResponse struct {
	Days      []string      `json:"days"`
	Day       string        `json:"day"`
	Events    []audit.Event `json:"events"`
	Dropped   int           `json:"dropped,omitempty"`
	Retention int           `json:"retention"`
}

// handleAudit returns the stored events of a day (default: the newest).
func (a *api) handleAudit(w http.ResponseWriter, r *http.Request) {
	out := auditResponse{Days: []string{}, Events: []audit.Event{}}
	if a.deps.Audit == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Retention = a.deps.Audit.Retention()
	day := r.URL.Query().Get("day")
	if day != "" && !audit.ValidDay(day) {
		writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
		return
	}
	days, err := a.deps.Audit.Days(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	out.Days = days
	if day == "" && len(days) > 0 {
		day = days[0]
	}
	out.Day = day
	if day != "" {
		d, err := a.deps.Audit.Get(r.Context(), day)
		if err != nil {
			a.writeErr(w, err)
			return
		}
		out.Events, out.Dropped = d.Events, d.Dropped
	}
	writeJSON(w, http.StatusOK, out)
}
