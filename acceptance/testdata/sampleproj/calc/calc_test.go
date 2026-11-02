package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Errorf("Add(2, 3) = %d, want 5", got)
	}
}

func TestDiv(t *testing.T) {
	if _, err := Div(1, 0); err != ErrDivByZero {
		t.Errorf("Div(1, 0) error = %v, want ErrDivByZero", err)
	}
}
