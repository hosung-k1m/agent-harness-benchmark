package model

import "testing"

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
