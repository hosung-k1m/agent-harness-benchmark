package state

import (
	"testing"
	"time"
)

func record() Record {
	return Record{Kind: "preflight", VariantID: "codex-cli", ConfigHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now().UTC(), Success: true}
}
func TestImmutableAndCurrent(t *testing.T) {
	root := t.TempDir()
	r := record()
	if e := Write(root, r); e != nil {
		t.Fatal(e)
	}
	if !Current(root, r.Kind, r.VariantID, r.ConfigHash) {
		t.Fatal("not current")
	}
	if e := Write(root, r); e == nil {
		t.Fatal("state overwrite allowed")
	}
	if Current(root, r.Kind, r.VariantID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatal("stale state current")
	}
}

func TestRejectsUnsafeStateIdentity(t *testing.T) {
	r := record()
	r.VariantID = "../outside"
	if err := r.Validate(); err == nil {
		t.Fatal("unsafe variant id accepted")
	}
	r = record()
	r.ConfigHash = "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"
	if err := r.Validate(); err == nil {
		t.Fatal("non-hex config hash accepted")
	}
	if _, err := Read(t.TempDir(), "preflight", "../outside", record().ConfigHash); err == nil {
		t.Fatal("unsafe read identity accepted")
	}
}
