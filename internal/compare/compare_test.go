package compare

import (
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"strings"
	"testing"
)

func TestMedianUnavailable(t *testing.T) {
	if Median([]*int64{nil, nil}) != nil {
		t.Fatal("expected nil")
	}
	a, b := int64(1), int64(9)
	if *Median([]*int64{&a, nil, &b}) != 5 {
		t.Fatal("bad median")
	}
}
func TestOrderReproducible(t *testing.T) {
	a := Order([]string{"a", "b"}, 123)
	b := Order([]string{"a", "b"}, 123)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatal("order not seeded")
	}
}
func TestMarkdown(t *testing.T) {
	pass := true
	s := Summary{Label: "Codex CLI", Results: []model.Result{{Status: model.StatusCompleted, VerifierPassed: &pass, ElapsedMillis: 12, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}}
	if !strings.Contains(RenderMarkdown(3, []Summary{s}), "—") {
		t.Fatal("missing unavailable glyph")
	}
}
