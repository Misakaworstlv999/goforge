package workflow

import "testing"

func TestAcceptanceReducer(t *testing.T) {
	seed := []AcceptancePoint{
		{ID: "AP-1", Description: "adds two numbers", Kind: KindUnit},
		{ID: "AP-2", Description: "end to end", Kind: KindE2E},
	}

	merged := acceptanceReducer(nil, seed).([]AcceptancePoint)
	if len(merged) != 2 || merged[0].ID != "AP-1" || merged[1].ID != "AP-2" {
		t.Fatalf("seed wrong: %+v", merged)
	}

	// A test stage patches AP-1 to pass; Kind/Description must be preserved and
	// insertion order must hold.
	upd := []AcceptancePoint{{ID: "AP-1", Status: StatusPass, Evidence: "go test ok"}}
	out := acceptanceReducer(merged, upd).([]AcceptancePoint)
	if len(out) != 2 {
		t.Fatalf("expected 2 points, got %d", len(out))
	}
	ap1 := out[0]
	if ap1.ID != "AP-1" || ap1.Status != StatusPass || ap1.Evidence != "go test ok" {
		t.Errorf("AP-1 not updated: %+v", ap1)
	}
	if ap1.Kind != KindUnit || ap1.Description != "adds two numbers" {
		t.Errorf("AP-1 lost preserved fields: %+v", ap1)
	}
	if out[1].ID != "AP-2" || out[1].Status != StatusPending {
		t.Errorf("AP-2 should be untouched/pending: %+v", out[1])
	}
}

func TestDecodeJSON_tolerant(t *testing.T) {
	// Fenced + surrounded by prose, kind as a string — what an LLM emits.
	raw := "Here is the spec:\n```json\n" +
		`{"summary":"add fn","scope":["math"],"acceptance":[{"id":"AP-1","description":"adds","kind":"unit"}]}` +
		"\n```\nDone."
	spec, err := decodeJSON[Spec](raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Summary != "add fn" || len(spec.Acceptance) != 1 {
		t.Fatalf("decoded spec wrong: %+v", spec)
	}
	if spec.Acceptance[0].Kind != KindUnit {
		t.Errorf("kind = %v, want unit", spec.Acceptance[0].Kind)
	}

	if _, err := decodeJSON[Spec]("no json here"); err == nil {
		t.Error("expected error when no JSON object present")
	}
}

func TestAcceptanceKind_jsonRoundTrip(t *testing.T) {
	for _, k := range []AcceptanceKind{KindUnit, KindIntegration, KindE2E, KindManual} {
		b, err := k.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var got AcceptanceKind
		if err := got.UnmarshalJSON(b); err != nil {
			t.Fatal(err)
		}
		if got != k {
			t.Errorf("round-trip %v → %s → %v", k, b, got)
		}
	}
}
