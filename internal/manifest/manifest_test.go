package manifest

import "testing"

func valid() Variant {
	return Variant{ID: "codex-cli", Image: "x", ImageDigest: "sha256:x", Adapter: "a", Model: Model, ReasoningEffort: Reasoning, NetworkPolicyID: NetworkPolicy, ProtectedPaths: []string{"tests/", "README.md"}}
}
func TestHashCanonical(t *testing.T) {
	a := valid()
	b := valid()
	b.ProtectedPaths = []string{"README.md", "tests/"}
	ha, e := Hash(a)
	if e != nil {
		t.Fatal(e)
	}
	hb, e := Hash(b)
	if e != nil || ha != hb {
		t.Fatalf("hashes %q %q %v", ha, hb, e)
	}
}
func TestRejectsUnpinned(t *testing.T) {
	v := valid()
	v.Model = "other"
	if v.Validate() == nil {
		t.Fatal("accepted unpinned model")
	}
}

func TestRejectsUnsafeOrDuplicateProtectedPaths(t *testing.T) {
	v := valid()
	v.ProtectedPaths = []string{"tests/", "tests"}
	if err := v.Validate(); err == nil {
		t.Fatal("duplicate protected path accepted")
	}
	v = valid()
	v.ProtectedPaths = []string{"../tests"}
	if err := v.Validate(); err == nil {
		t.Fatal("traversal protected path accepted")
	}
}
