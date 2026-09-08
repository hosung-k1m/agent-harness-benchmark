package model

import (
	"encoding/json"
	"testing"
)

func TestClassify(t *testing.T) {
	if got := Classify(true, false, false, false); got != StatusCompleted {
		t.Fatal(got)
	}
	if got := Classify(true, false, true, true); got != StatusInfrastructureInvalid {
		t.Fatal(got)
	}
	if got := Classify(false, true, false, false); got != StatusTimedOut {
		t.Fatal(got)
	}
}
func TestUsageNulls(t *testing.T) {
	u := Usage{TokenQuality: TokenUnavailable}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	u.InputTokens = Int64(0)
	if err := u.Validate(); err == nil {
		t.Fatal("expected unavailable fields rejection")
	}
}

func TestResultBenchmarkMetadataValidationIsAdditive(t *testing.T) {
	legacy := Result{Status: StatusCompleted, Usage: Usage{TokenQuality: TokenUnavailable}}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy result: %v", err)
	}
	result := legacy
	result.BenchmarkID, result.BenchmarkTaskID = "bfcl", "get-weather"
	result.Provenance = json.RawMessage(`{"source_id":"weather-1"}`)
	result.Evaluator = json.RawMessage(`{"type":"trusted-hidden-verifier"}`)
	if err := result.Validate(); err != nil {
		t.Fatalf("benchmark result: %v", err)
	}
	result.BenchmarkTaskID = ""
	if err := result.Validate(); err == nil {
		t.Fatal("expected unpaired benchmark metadata rejection")
	}
	result.BenchmarkTaskID = "get-weather"
	result.Provenance = json.RawMessage(`not-json`)
	if err := result.Validate(); err == nil {
		t.Fatal("expected invalid metadata rejection")
	}
}

func TestDeterministicVerdictMustMatchVerifier(t *testing.T) {
	passed := true
	result := Result{Status: StatusCompleted, Usage: Usage{TokenQuality: TokenUnavailable}, VerifierPassed: &passed, Verdict: "pass"}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid verdict: %v", err)
	}
	result.Verdict = "fail"
	if err := result.Validate(); err == nil {
		t.Fatal("expected mismatched verdict rejection")
	}
	result.Verdict = "maybe"
	if err := result.Validate(); err == nil {
		t.Fatal("expected unknown verdict rejection")
	}
}
