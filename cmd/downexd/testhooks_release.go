//go:build !e2etest

package main

// loadTestHooks returns no hooks: this is a product build.
func loadTestHooks() (testHooks, error) { return testHooks{}, nil }
