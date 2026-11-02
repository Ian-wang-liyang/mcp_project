// Package calc does arithmetic.
package calc

import "errors"

// ErrDivByZero is returned by Div when b is zero.
var ErrDivByZero = errors.New("division by zero")

// Add returns a + b.
func Add(a, b int) int { return a + b }

// Div returns a / b, or ErrDivByZero.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivByZero
	}
	return a / b, nil
}
