package streams

import (
	"fmt"
	"sort"
	"strings"
)

// Step fields available to later steps as ${{ steps.<id>.<field> }}.
var stepFields = map[string]bool{"status": true, "runId": true, "url": true, "sha": true, "ref": true}

// Built-in values available to every step.
var builtins = map[string]bool{"zea.user": true, "stream.name": true, "stream.run": true}

const (
	openDelim  = "${{"
	closeDelim = "}}"
)

// References returns the trimmed contents of every ${{ ... }} in v.
func References(v string) ([]string, error) {
	var out []string
	rest := v
	for {
		i := strings.Index(rest, openDelim)
		if i < 0 {
			return out, nil
		}
		rest = rest[i+len(openDelim):]
		j := strings.Index(rest, closeDelim)
		if j < 0 {
			return out, fmt.Errorf("unclosed %q", openDelim)
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		rest = rest[j+len(closeDelim):]
	}
}

// checkTemplate reports references that are malformed or point to unknown
// params or to steps that do not run before this one.
func checkTemplate(v string, params, upstream map[string]bool) []string {
	refs, err := References(v)
	var out []string
	if err != nil {
		out = append(out, err.Error())
	}
	for _, ref := range refs {
		if msg := checkReference(ref, params, upstream); msg != "" {
			out = append(out, msg)
		}
	}
	return out
}

func checkReference(ref string, params, upstream map[string]bool) string {
	if ref == "" {
		return "empty ${{ }}"
	}
	if builtins[ref] {
		return ""
	}
	parts := strings.Split(ref, ".")
	switch {
	case parts[0] == "params" && len(parts) == 2:
		if !params[parts[1]] {
			return fmt.Sprintf("unknown param %q", parts[1])
		}
		return ""
	case parts[0] == "steps" && len(parts) == 3:
		if !upstream[parts[1]] {
			return fmt.Sprintf("step %q does not finish before this step (add it to needs or move it to an earlier stage)", parts[1])
		}
		if !stepFields[parts[2]] {
			return fmt.Sprintf("unknown step field %q: use status, runId, url, sha or ref", parts[2])
		}
		return ""
	}
	return fmt.Sprintf("unknown reference %q: use params.<name>, steps.<id>.<field>, zea.user, stream.name or stream.run", ref)
}

// Render replaces every ${{ ref }} in v with values[ref].
func Render(v string, values map[string]string) (string, error) {
	var b strings.Builder
	rest := v
	for {
		i := strings.Index(rest, openDelim)
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(openDelim):]
		j := strings.Index(rest, closeDelim)
		if j < 0 {
			return "", fmt.Errorf("unclosed %q", openDelim)
		}
		ref := strings.TrimSpace(rest[:j])
		val, ok := values[ref]
		if !ok {
			return "", fmt.Errorf("no value for %q", ref)
		}
		b.WriteString(val)
		rest = rest[j+len(closeDelim):]
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
