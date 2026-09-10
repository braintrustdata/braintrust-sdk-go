package mustache

import (
	"encoding/json"
	"testing"
)

// These tests cover the Braintrust modifications to the vendored library. See
// VENDOR.md. Upstream's own suite lives in mustache_test.go.

func jsonValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func renderWithJSONValues(t *testing.T, tmplText string, ctx interface{}) string {
	t.Helper()
	tmpl, err := ParseString(tmplText)
	if err != nil {
		t.Fatalf("parse %q: %v", tmplText, err)
	}
	tmpl.Escape(func(s string) string { return s })
	tmpl.Value(jsonValue)
	out, err := tmpl.Render(ctx)
	if err != nil {
		t.Fatalf("render %q: %v", tmplText, err)
	}
	return out
}

func TestValueFunc(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		ctx  map[string]interface{}
		want string
	}{
		{
			name: "string is passed through untouched",
			tmpl: "{{v}}",
			ctx:  map[string]interface{}{"v": "hello"},
			want: "hello",
		},
		{
			name: "map is JSON encoded, not Go syntax",
			tmpl: "{{v}}",
			ctx:  map[string]interface{}{"v": map[string]interface{}{"a": 1}},
			want: `{"a":1}`,
		},
		{
			name: "slice is JSON encoded",
			tmpl: "{{v}}",
			ctx:  map[string]interface{}{"v": []interface{}{"a", "b"}},
			want: `["a","b"]`,
		},
		{
			name: "raw interpolation uses the same value func",
			tmpl: "{{{v}}}",
			ctx:  map[string]interface{}{"v": map[string]interface{}{"a": 1}},
			want: `{"a":1}`,
		},
		{
			name: "numbers keep their JSON form",
			tmpl: "{{v}}",
			ctx:  map[string]interface{}{"v": 1.5},
			want: "1.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderWithJSONValues(t, tt.tmpl, tt.ctx); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEscapeFuncDisablesHTMLEscaping(t *testing.T) {
	// The upstream default HTML-escapes {{var}}, which mangles prompt text.
	ctx := map[string]interface{}{"v": `5 > 3 && "quoted"`}

	if got := renderWithJSONValues(t, "{{v}}", ctx); got != `5 > 3 && "quoted"` {
		t.Errorf("custom escape not applied: got %q", got)
	}

	def, err := Render("{{v}}", ctx)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if def == `5 > 3 && "quoted"` {
		t.Error("expected the vendored default to still HTML-escape")
	}
}

func TestMissingVariableRendersEmpty(t *testing.T) {
	// Non-strict rendering: a miss is an empty string, matching chevron and
	// mustache.js. Strict mode is implemented in the prompt package on top of
	// Tags(), so this global stays at its default.
	if got := renderWithJSONValues(t, "Hello {{name}}!", map[string]interface{}{}); got != "Hello !" {
		t.Errorf("got %q, want %q", got, "Hello !")
	}
}

func TestTagsReportsVariableNames(t *testing.T) {
	// The prompt package's strict mode walks Tags() to find missing variables.
	tmpl, err := ParseString("{{a}} {{{b}}} {{#c}}{{d}}{{/c}} {{! comment }}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var names []string
	var walk func(tags []Tag)
	walk = func(tags []Tag) {
		for _, tag := range tags {
			names = append(names, tag.Name())
			// Tags() panics on anything but a section, which is why the prompt
			// package's strict lint checks Type() before recursing.
			if tag.Type() == Section || tag.Type() == InvertedSection {
				walk(tag.Tags())
			}
		}
	}
	walk(tmpl.Tags())

	want := []string{"a", "b", "c", "d"}
	if len(names) != len(want) {
		t.Fatalf("got tags %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got tags %v, want %v", names, want)
		}
	}
}
