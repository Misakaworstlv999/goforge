package tool

import (
	"context"
	"sort"
	"strings"
	"testing"
)

func filterTool(name string) Tool {
	return NewTool(name, "d", func(context.Context, struct{}) (string, error) { return "", nil })
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	sort.Strings(out)
	return out
}

func TestFilters(t *testing.T) {
	all := []Tool{
		filterTool("read_file"), filterTool("write_file"),
		filterTool("github_create_issue"), filterTool("github_read_repo"),
		filterTool("km_corp_search"),
	}
	keep := func(f Filter) []string {
		var out []Tool
		for _, tl := range all {
			if f(tl) {
				out = append(out, tl)
			}
		}
		return names(out)
	}

	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"Names", Names("read_file", "write_file"), []string{"read_file", "write_file"}},
		{"ExcludeNames", ExcludeNames("github_create_issue", "github_read_repo", "km_corp_search"), []string{"read_file", "write_file"}},
		{"Prefix server", Prefix("github_"), []string{"github_create_issue", "github_read_repo"}},
		{"Glob read across servers", Glob("*read*"), []string{"github_read_repo", "read_file"}},
		{"Any", Any(Prefix("github_"), Names("read_file")), []string{"github_create_issue", "github_read_repo", "read_file"}},
		{"All", All(Prefix("github_"), Glob("*read*")), []string{"github_read_repo"}},
		{"Not", Not(Prefix("github_")), []string{"km_corp_search", "read_file", "write_file"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keep(tc.f); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRegistry_Subset(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(filterTool("read_file"), filterTool("github_a"), filterTool("github_b")); err != nil {
		t.Fatal(err)
	}

	sub := reg.Subset(Prefix("github_"))
	if len(sub.Schemas()) != 2 {
		t.Errorf("Subset(Prefix) = %d tools, want 2", len(sub.Schemas()))
	}
	// Source registry is untouched.
	if len(reg.Schemas()) != 3 {
		t.Errorf("source registry mutated: %d", len(reg.Schemas()))
	}
	// Select-matching: a pattern matching nothing yields an empty registry, no error.
	if empty := reg.Subset(Prefix("nope_")); len(empty.Schemas()) != 0 {
		t.Errorf("non-matching subset should be empty, got %d", len(empty.Schemas()))
	}
	// nil keep ⇒ all tools.
	if cp := reg.Subset(nil); len(cp.Schemas()) != 3 {
		t.Errorf("nil filter should keep all, got %d", len(cp.Schemas()))
	}
}
