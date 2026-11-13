package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"

	"connectrpc.com/connect"
)

// freeSpaceRequest is the argument of FreeSpace.
type freeSpaceRequest struct {
	Dir string `json:"dir"`
}

// freeSpaceResponse is its result: the bytes as a decimal string, like protojson's int64.
type freeSpaceResponse struct {
	FreeBytes string `json:"freeBytes"`
}

// FreeSpace returns the bytes available to the user on the volume that holds a folder
// ({"dir": "<absolute path>"} → {"freeBytes": "<int64>"}): the status bar's "D: 214 GiB
// free" and the Add dialog's free space (docs/DESIGN-SYSTEM.md §3.14, §6.2). A folder
// that does not exist yet is measured at its nearest existing parent. It only reads: an
// empty or relative dir is refused with invalid_argument, and a failed read is unavailable.
// The folder is never logged, and no error repeats it.
func (a *App) FreeSpace(req json.RawMessage) (json.RawMessage, error) {
	var in freeSpaceRequest
	if !isEmptyJSON(req) {
		dec := json.NewDecoder(bytes.NewReader(req))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			// The decoder's text can quote the request; the reason alone is enough.
			return nil, apiError(connect.NewError(connect.CodeInvalidArgument, errors.New("malformed request")))
		}
	}
	if in.Dir == "" || !filepath.IsAbs(in.Dir) {
		return nil, apiError(connect.NewError(connect.CodeInvalidArgument, errors.New("dir must be an absolute path")))
	}
	free, err := a.freeSpace(in.Dir)
	if err != nil || free < 0 {
		// platform.FreeSpace's errors name the folder: none of it leaves this function.
		return nil, apiError(connect.NewError(connect.CodeUnavailable, errors.New("could not read the free space")))
	}
	b, err := json.Marshal(freeSpaceResponse{FreeBytes: strconv.FormatInt(free, 10)})
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, errors.New("could not encode the free space")))
	}
	return b, nil
}
