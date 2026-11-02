package broken

import "testing"

func TestDouble(t *testing.T) {
	if got := Double(5); got != 10 {
		t.Errorf("Double(5) = %d, want 10", got)
	}
}

func TestDoubleZero(t *testing.T) {
	if got := Double(0); got != 0 {
		t.Errorf("Double(0) = %d, want 0", got)
	}
}
