//go:build windows

package e2e

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// readOriginMark returns the Zone.Identifier stream of path (Mark-of-the-Web), after
// checking that it names the Internet zone.
func readOriginMark(path string) (string, error) {
	b, err := os.ReadFile(path + ":Zone.Identifier")
	switch {
	case errors.Is(err, windows.ERROR_INVALID_NAME), errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return "", errNoMarks // a volume without alternate data streams (FAT)
	case err != nil:
		return "", err
	}
	s := string(b)
	if !strings.HasPrefix(s, "[ZoneTransfer]\r\n") || !strings.Contains(s, "ZoneId=3\r\n") {
		return s, errors.New("not an Internet-zone Zone.Identifier: " + s)
	}
	return s, nil
}
