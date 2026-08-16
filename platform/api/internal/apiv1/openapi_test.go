package apiv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openapi.yaml is the written contract between this API and the portal, and
// the plan treats "it parses and every $ref resolves" as a standing invariant.
// It was not true: a responses: key under /api/devices/cli was indented by
// seven spaces instead of six, so the whole document failed to parse, and
// nothing noticed because nothing read it. These tests read it.

func loadSpec(t *testing.T) (map[string]any, string) {
	t.Helper()

	path := filepath.Join("..", "..", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	return spec, string(raw)
}

func TestOpenAPIParses(t *testing.T) {
	spec, _ := loadSpec(t)

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("openapi.yaml has no paths object")
	}
	if len(paths) == 0 {
		t.Fatal("openapi.yaml declares no paths")
	}
}

func TestOpenAPIRefsResolve(t *testing.T) {
	spec, _ := loadSpec(t)

	var refs []string
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for key, value := range n {
				if key == "$ref" {
					if ref, ok := value.(string); ok {
						refs = append(refs, ref)
					}
					continue
				}
				walk(value)
			}
		case []any:
			for _, value := range n {
				walk(value)
			}
		}
	}
	walk(spec)

	if len(refs) == 0 {
		t.Fatal("no $refs found, which means this test is not checking anything")
	}

	for _, ref := range refs {
		if !strings.HasPrefix(ref, "#/") {
			t.Errorf("%s: only local refs are supported", ref)
			continue
		}

		node := any(spec)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			obj, ok := node.(map[string]any)
			if !ok {
				t.Errorf("%s: does not resolve, %q is not an object", ref, part)
				node = nil
				break
			}
			node, ok = obj[part]
			if !ok {
				t.Errorf("%s: does not resolve, %q is missing", ref, part)
				node = nil
				break
			}
		}
	}
}

// A schema defined twice is not a parse error: YAML keeps the last one and
// discards the first silently. That is how the portal's Device shape came to
// replace the RustDesk client's.
func TestOpenAPIHasNoDuplicateSchemas(t *testing.T) {
	_, raw := loadSpec(t)

	inComponents := false
	inSchemas := false
	seen := map[string]bool{}

	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "components:") {
			inComponents = true
			continue
		}
		if !inComponents {
			continue
		}
		// A two-space key starts a new components subsection.
		if len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' {
			inSchemas = strings.TrimSpace(line) == "schemas:"
			continue
		}
		if !inSchemas {
			continue
		}
		if name, ok := strings.CutSuffix(strings.TrimPrefix(line, "    "), ":"); ok {
			if strings.HasPrefix(line, "     ") || strings.ContainsAny(name, " #") || name == "" {
				continue
			}
			if seen[name] {
				t.Errorf("schema %q is defined more than once; the later definition silently wins", name)
			}
			seen[name] = true
		}
	}

	if len(seen) == 0 {
		t.Fatal("found no schemas, which means this test is not checking anything")
	}
}

// Every route the handler serves should be documented, or the spec stops being
// the contract the portal's client is generated from.
func TestEveryRouteIsInTheSpec(t *testing.T) {
	spec, _ := loadSpec(t)

	paths, _ := spec["paths"].(map[string]any)

	for _, route := range (&Handler{}).Routes() {
		method, path, _ := strings.Cut(route.Pattern, " ")

		documented, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("%s is served but not documented in openapi.yaml", path)
			continue
		}
		if _, ok := documented[strings.ToLower(method)]; !ok {
			t.Errorf("%s %s is served but the spec documents no %s operation", method, path, method)
		}
	}
}
