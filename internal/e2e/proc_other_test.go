//go:build !windows && !unix

package e2e

// processAlive cannot probe processes here; it reports them gone.
func processAlive(int) bool { return false }
