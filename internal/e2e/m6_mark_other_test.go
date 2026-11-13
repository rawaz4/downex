//go:build !windows && !darwin && !linux

package e2e

// readOriginMark: these systems keep no origin marks (internal/filemark).
func readOriginMark(string) (string, error) { return "", errNoMarks }
