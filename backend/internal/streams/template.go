package streams

import (
	"fmt"
	"sort"
	"strings"
)

// Step fields available to later steps as ${{ steps.<id>.<field> }}.
// user is who decided an approval step; revision, health and images are
// the Application state of an argocd step.
var stepFields = map[string]bool{
	"status": true, "runId": true, "url": true, "sha": true, "ref": true, "user": true,
	"revision": true, "health": true, "images": true,
}

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

// stepValues are the inputs and variables of the step a value belongs to,
// for ${{ var.name.<key> }} and ${{ var.value.<key> }}. usable holds the
// keys that may be referenced (set, and not using var.* themselves); all
// holds every key. A nil stepValues means var.* is not allowed.
type stepValues struct {
	usable, all map[string]bool
}

// checkTemplate reports references that are malformed or point to unknown
// params, to steps that do not run before this one or to values the step
// does not set.
func checkTemplate(v string, params, upstream map[string]bool, own *stepValues) []string {
	refs, err := References(v)
	var out []string
	if err != nil {
		out = append(out, err.Error())
	}
	for _, ref := range refs {
		if msg := checkReference(ref, params, upstream, own); msg != "" {
			out = append(out, msg)
		}
	}
	return out
}

func checkReference(ref string, params, upstream map[string]bool, own *stepValues) string {
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
	case parts[0] == "var" && len(parts) == 3 && (parts[1] == "name" || parts[1] == "value"):
		switch {
		case own == nil:
			return "var.* can only be used in the inputs and variables of a pipeline step"
		case own.usable[parts[2]]:
			return ""
		case own.all[parts[2]]:
			return fmt.Sprintf("%q uses var.* itself, so it cannot be referenced with var.*", parts[2])
		}
		return fmt.Sprintf("%q is not an input or variable set in this step", parts[2])
	case parts[0] == "steps" && len(parts) == 3:
		if !upstream[parts[1]] {
			return fmt.Sprintf("step %q does not finish before this step (add it to needs or move it to an earlier stage)", parts[1])
		}
		if !stepFields[parts[2]] {
			return fmt.Sprintf("unknown step field %q: use status, runId, url, sha, ref, user, revision, health or images", parts[2])
		}
		return ""
	}
	return fmt.Sprintf("unknown reference %q: use params.<name>, steps.<id>.<field>, var.name.<key>, var.value.<key>, zea.user, stream.name or stream.run", ref)
}

// usesStepValues reports whether v references var.name.* or var.value.*.
func usesStepValues(v string) bool {
	refs, _ := References(v)
	for _, ref := range refs {
		if strings.HasPrefix(ref, "var.") {
			return true
		}
	}
	return false
}

// isOn reports whether a value counts as switched on for var.name: not
// empty and not false, off, no or 0 (any case).
func isOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "off", "no", "0":
		return false
	}
	return true
}

// renderStepValues renders the inputs and variables of a step. Values that
// use var.* are rendered last, from the rendered values that do not:
// var.value.<key> is the value of key (a variable wins over an input of the
// same name) and var.name.<key> is key when that value is on, else empty.
func renderStepValues(inputs, variables, vals map[string]string) (map[string]string, map[string]string, error) {
	in, err := renderPlain(inputs, vals)
	if err != nil {
		return nil, nil, fmt.Errorf("inputs: %w", err)
	}
	vars, err := renderPlain(variables, vals)
	if err != nil {
		return nil, nil, fmt.Errorf("variables: %w", err)
	}
	ext := make(map[string]string, len(vals)+2*(len(in)+len(vars)))
	for k, v := range vals {
		ext[k] = v
	}
	for _, m := range []map[string]string{in, vars} {
		for k, v := range m {
			ext["var.value."+k] = v
			ext["var.name."+k] = ""
			if isOn(v) {
				ext["var.name."+k] = k
			}
		}
	}
	for _, x := range []struct {
		field    string
		src, dst map[string]string
	}{{"inputs", inputs, in}, {"variables", variables, vars}} {
		for _, k := range sortedKeys(x.src) {
			if !usesStepValues(x.src[k]) {
				continue
			}
			v, err := Render(x.src[k], ext)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s: %w", x.field, k, err)
			}
			x.dst[k] = v
		}
	}
	if len(in) == 0 {
		in = nil
	}
	if len(vars) == 0 {
		vars = nil
	}
	return in, vars, nil
}

// renderPlain renders the values of m that do not use var.*.
func renderPlain(m, vals map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		if usesStepValues(m[k]) {
			continue
		}
		v, err := Render(m[k], vals)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
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
