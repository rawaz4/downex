package mpegts

import (
	"context"
	"errors"
	"io"
)

var errProbed = errors.New("mpegts: program found")

// Probe returns the first program of the first PAT in prefix (with its first PMT
// version). It returns an *Error of kind ErrMalformed when prefix holds no PAT and
// PMT of that program, or when sync is lost beyond the resync window before them,
// and of kind media.ErrEncrypted when the program-specific information is
// scrambled.
func Probe(prefix []byte) (Program, error) {
	var first Program
	found := false
	d := NewBytesDemuxer([][]byte{prefix}, nil, WithProgramCheck(func(p Program) error {
		first, found = p, true
		return errProbed
	}))
	defer d.Close()
	_, err := d.Next(context.Background())
	if found {
		return first, nil
	}
	if err == io.EOF || err == nil {
		return Program{}, newError(ErrMalformed, -1, -1, "no program association and program map table found")
	}
	return Program{}, err
}
