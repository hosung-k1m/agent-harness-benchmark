package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWritePlanPersistsSeedOrderAndAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	want := plan{
		Case:     "slugify-v1",
		Trials:   1,
		Seed:     42,
		Orders:   [][]string{{"dsh-default-codex", "codex-cli"}},
		Attempts: []planAttempt{{Trial: 1, Variant: "dsh-default-codex", Artifact: "../runs/a/result.json"}},
	}
	if err := writePlan(path, want); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got plan
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted plan mismatch: %#v", got)
	}
}

func TestComparisonVariantIDs(t *testing.T) {
	want := []string{"codex-cli", "dsh-default-codex", "dsh-modified-codex"}
	if got := comparisonVariantIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("comparison variants = %#v, want %#v", got, want)
	}
}
