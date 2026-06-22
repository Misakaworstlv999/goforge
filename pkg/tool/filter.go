package tool

import (
	"path"
	"strings"
)

// Filter is a predicate selecting tools by their identity. It is the general
// mechanism for scoping a subset of tools to an agent WITHOUT enumerating every
// name — essential when an MCP server contributes dozens of tools. Mirrors the
// predicate approach in ADK (Predicate) and tRPC-Agent-Go (FilterFunc); the
// name-list selectors below are conveniences built on top.
type Filter func(t Tool) bool

// Names selects exactly the named tools (an allowlist).
func Names(names ...string) Filter {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return func(t Tool) bool {
		_, ok := set[t.Name()]
		return ok
	}
}

// ExcludeNames selects every tool except the named ones (a denylist).
func ExcludeNames(names ...string) Filter {
	return Not(Names(names...))
}

// Prefix selects tools whose name starts with p. Because MCP tools are namespaced
// as "<server>_<tool>" (see mcpclient), Prefix("github_") selects an entire MCP
// server's toolset in one expression.
func Prefix(p string) Filter {
	return func(t Tool) bool { return strings.HasPrefix(t.Name(), p) }
}

// Glob selects tools whose name matches a shell-style pattern (path.Match), e.g.
// Glob("*_read_*") for read-style tools across servers.
func Glob(pattern string) Filter {
	return func(t Tool) bool {
		ok, err := path.Match(pattern, t.Name())
		return err == nil && ok
	}
}

// Any is the union: a tool is kept if it passes any of the filters.
func Any(filters ...Filter) Filter {
	return func(t Tool) bool {
		for _, f := range filters {
			if f(t) {
				return true
			}
		}
		return false
	}
}

// All is the intersection: a tool is kept only if it passes every filter.
func All(filters ...Filter) Filter {
	return func(t Tool) bool {
		for _, f := range filters {
			if !f(t) {
				return false
			}
		}
		return true
	}
}

// Not negates a filter.
func Not(f Filter) Filter {
	return func(t Tool) bool { return !f(t) }
}

// Subset returns a new Registry containing only the tools that pass keep. It is
// "select-matching": tools that don't match are simply omitted (no error), so a
// pattern that matches nothing yields an empty registry rather than failing.
// A nil keep returns a copy containing all tools.
func (r *Registry) Subset(keep Filter) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := NewRegistry()
	for _, t := range r.tools {
		if keep == nil || keep(t) {
			sub.tools[t.Name()] = t
		}
	}
	return sub
}
