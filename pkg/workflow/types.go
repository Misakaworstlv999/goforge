// Package workflow implements the M6 dev workflow: a cyclic graph of specialized
// agent stages (requirement → techdesign → coding → review → test ×3 →
// acceptance) built on the Ring 4 pipeline engine. Its defining ideas:
//
//   - Acceptance points are defined UP FRONT during requirement analysis and
//     carried as a contract on the shared blackboard; the work is "done" only
//     when every point passes (acceptanceGate).
//   - Verification is PROGRESSIVE: unit → integration → e2e test stages, each a
//     gate that bounces failures back to coding (the rework hub).
package workflow

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strings"
)

func init() {
	// Blackboard artifacts must be registered so pipeline checkpoint gob encoding
	// succeeds after requirement/techdesign/coding stages persist typed values.
	gob.Register(Spec{})
	gob.Register(Design{})
	gob.Register(CodeChange{})
	gob.Register(TestReport{})
	gob.Register(AcceptancePoint{})
	gob.Register(AcceptanceKind(0))
	gob.Register(AcceptanceStatus(0))
	gob.Register(LayerResult{})
	gob.Register([]AcceptancePoint(nil))
	gob.Register([]LayerResult(nil))
}

// AcceptanceKind is the test layer that proves an acceptance point.
type AcceptanceKind int

const (
	KindUnit AcceptanceKind = iota
	KindIntegration
	KindE2E
	KindManual
)

func (k AcceptanceKind) String() string {
	switch k {
	case KindUnit:
		return "unit"
	case KindIntegration:
		return "integration"
	case KindE2E:
		return "e2e"
	case KindManual:
		return "manual"
	default:
		return "unknown"
	}
}

// MarshalJSON / UnmarshalJSON let acceptance kinds round-trip as readable strings
// ("unit"/"integration"/"e2e"/"manual"), which is what an LLM naturally emits;
// a bare number is also accepted on input for robustness.
func (k AcceptanceKind) MarshalJSON() ([]byte, error) {
	return []byte(`"` + k.String() + `"`), nil
}

func (k *AcceptanceKind) UnmarshalJSON(b []byte) error {
	switch s := strings.ToLower(strings.Trim(string(b), `"`)); s {
	case "unit", "0":
		*k = KindUnit
	case "integration", "1":
		*k = KindIntegration
	case "e2e", "2":
		*k = KindE2E
	case "manual", "3":
		*k = KindManual
	default:
		return fmt.Errorf("unknown acceptance kind %q", s)
	}
	return nil
}

// AcceptanceStatus is the verification state of an acceptance point.
type AcceptanceStatus int

const (
	StatusPending AcceptanceStatus = iota
	StatusPass
	StatusFail
)

func (s AcceptanceStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusPass:
		return "pass"
	case StatusFail:
		return "fail"
	default:
		return "unknown"
	}
}

// AcceptancePoint is one verifiable acceptance criterion. It is defined during
// requirement analysis (the contract) and its Status is updated by the matching
// test layer. Kind binds it to the test layer responsible for proving it.
type AcceptancePoint struct {
	ID          string           `json:"id"`
	Description string           `json:"description"`
	Kind        AcceptanceKind   `json:"kind"`
	Status      AcceptanceStatus `json:"-"`
	Evidence    string           `json:"-"`
}

// Spec is the requirement stage's output: scope plus the acceptance contract.
type Spec struct {
	Summary    string            `json:"summary"`
	Scope      []string          `json:"scope"`
	Acceptance []AcceptancePoint `json:"acceptance"`
}

// Design is the techdesign stage's output. It carries the Spec forward so later
// stages retain the acceptance contract on the typed hand-off path too.
type Design struct {
	Approach string   `json:"approach"`
	Files    []string `json:"files"`
	Risks    []string `json:"risks"`
	Spec     Spec     `json:"-"`
}

// CodeChange is the coding stage's output: the files it wrote and a summary.
type CodeChange struct {
	Files   []string `json:"files"`
	Summary string   `json:"summary"`
	Design  Design   `json:"-"`
}

// LayerResult is one progressive test layer's outcome.
type LayerResult struct {
	Kind   AcceptanceKind
	Passed bool
	Output string
}

// TestReport is a test stage's output.
type TestReport struct {
	Layers     []LayerResult
	Acceptance []AcceptancePoint
}

// acceptanceKey is the shared-blackboard key holding the []AcceptancePoint
// contract. Stages read it (acceptance gate) and update it (test stages).
const acceptanceKey = "acceptance"

// acceptanceReducer merges acceptance-point updates by ID: the requirement stage
// seeds the full contract; test stages later Set partial updates (ID + Status +
// Evidence) which overwrite status/evidence while preserving Kind/Description and
// insertion order. Registered via State.SetReducer(acceptanceKey, ...).
func acceptanceReducer(old, new any) any {
	merged := map[string]AcceptancePoint{}
	var order []string
	absorb := func(pts []AcceptancePoint) {
		for _, p := range pts {
			cur, seen := merged[p.ID]
			if !seen {
				order = append(order, p.ID)
				merged[p.ID] = p
				continue
			}
			// Update from p, preserving fields it leaves unset.
			cur.Status = p.Status
			if p.Evidence != "" {
				cur.Evidence = p.Evidence
			}
			if p.Description != "" {
				cur.Description = p.Description
			}
			merged[p.ID] = cur
		}
	}
	if o, ok := old.([]AcceptancePoint); ok {
		absorb(o)
	}
	if n, ok := new.([]AcceptancePoint); ok {
		absorb(n)
	}
	out := make([]AcceptancePoint, 0, len(order))
	for _, id := range order {
		out = append(out, merged[id])
	}
	return out
}

// extractJSONObject returns the balanced {...} substring starting at start, or
// false when braces are unbalanced. String literals are respected so prose like
// api/{service}/{service}.proto does not swallow a later real JSON object.
func extractJSONObject(s string, start int) (string, bool) {
	if start < 0 || start >= len(s) || s[start] != '{' {
		return "", false
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// decodeJSON extracts a JSON object from s (tolerating Markdown fences or
// surrounding prose, as LLMs commonly emit) and unmarshals it into T. When
// multiple objects appear, the longest substring that unmarshals into T wins —
// this skips brace placeholders like {service} and nested fragments inside a
// larger payload.
func decodeJSON[T any](s string) (T, error) {
	var zero T
	var found T
	var ok bool
	var bestLen int
	var lastErr error
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		chunk, complete := extractJSONObject(s, i)
		if !complete {
			continue
		}
		var out T
		if err := json.Unmarshal([]byte(chunk), &out); err != nil {
			lastErr = err
			continue
		}
		if !ok || len(chunk) > bestLen {
			found = out
			bestLen = len(chunk)
			ok = true
		}
	}
	if ok {
		return found, nil
	}
	if lastErr != nil {
		return zero, fmt.Errorf("decoding JSON output: %w", lastErr)
	}
	return zero, fmt.Errorf("no JSON object found in output")
}
