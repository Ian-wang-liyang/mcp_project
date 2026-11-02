// Package broken has a bug that its test catches.
package broken

// Double should return 2*n. TODO: fix the bug.
func Double(n int) int { return n + 2 }
