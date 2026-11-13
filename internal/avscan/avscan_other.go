//go:build !windows

package avscan

import "os/exec"

// checkCommandPlatform: any regular file may be a scanner (its permissions decide).
func checkCommandPlatform(string) error { return nil }

// checkArgsPlatform checks that args can be split into words.
func checkArgsPlatform(args string) error {
	_, err := splitArgs(args)
	return err
}

// argv returns the scanner's arguments: args split into words, [File] in each
// replaced by the file path.
func argv(args, file string) ([]string, error) {
	words, err := splitArgs(args)
	if err != nil {
		return nil, err
	}
	for i, w := range words {
		words[i] = replacePlaceholder(w, file)
	}
	return words, nil
}

func setCommandLine(cmd *exec.Cmd, args, file string) error {
	words, err := argv(args, file)
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args[:1], words...)
	return nil
}
