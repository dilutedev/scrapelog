package storage

import "testing"

func TestEstimatedBytes(t *testing.T) {
	if got := estimatedBytes(25, 100, 1000); got != 250 {
		t.Errorf("proportional share: got %d, want 250", got)
	}
	if got := estimatedBytes(0, 0, 1000); got != 0 {
		t.Errorf("empty table: got %d, want 0", got)
	}
	if got := estimatedBytes(1000000000, 1000000000, 25000000000); got != 25000000000 {
		t.Errorf("large table: got %d, want 25000000000", got)
	}
}
