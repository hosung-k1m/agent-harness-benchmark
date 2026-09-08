package score

import "testing"

func TestClampUpper(t *testing.T) {
	if ClampScore(9, 0, 5) != 5 {
		t.Fatal("upper")
	}
}
