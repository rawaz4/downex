package formattest

import (
	"encoding/json"
	"errors"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// MediaInfoArg reads a lib/api.ts MediaInfo argument (fields it leaves out are zero).
func MediaInfoArg(raw json.RawMessage) (*downexv1.MediaInfo, error) {
	var m wireMedia
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m.proto(), nil
}

// TrackArg reads a lib/api.ts MediaTrack argument (fields it leaves out are zero).
func TrackArg(raw json.RawMessage) (*downexv1.MediaTrack, error) {
	var t wireTrack
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return t.proto(), nil
}

// TracksArg reads a MediaTrack[] argument.
func TracksArg(raw json.RawMessage) ([]*downexv1.MediaTrack, error) {
	var ts []wireTrack
	if err := json.Unmarshal(raw, &ts); err != nil {
		return nil, err
	}
	out := make([]*downexv1.MediaTrack, len(ts))
	for i, t := range ts {
		out[i] = t.proto()
	}
	return out, nil
}

func (t wireTrack) proto() *downexv1.MediaTrack {
	return &downexv1.MediaTrack{Id: t.ID, Kind: t.Kind, Bandwidth: t.Bandwidth, Width: t.Width, Height: t.Height,
		FrameRate: t.FrameRate, Codecs: t.Codecs, Language: t.Language, Name: t.Name, Size: t.Size,
		SizeApprox: t.SizeApprox, Ext: t.Ext}
}

// TrackJSON is a track as lib/wails.ts hands it to the page: every field of api.ts's
// MediaTrack; nil stays nil (undefined).
func TrackJSON(t *downexv1.MediaTrack) any {
	if t == nil {
		return Undefined
	}
	return map[string]any{"id": t.GetId(), "kind": t.GetKind(), "bandwidth": t.GetBandwidth(), "width": t.GetWidth(),
		"height": t.GetHeight(), "frameRate": t.GetFrameRate(), "codecs": t.GetCodecs(), "language": t.GetLanguage(),
		"name": t.GetName(), "size": t.GetSize(), "sizeApprox": t.GetSizeApprox(), "ext": t.GetExt()}
}

// TracksJSON is TrackJSON of each track.
func TracksJSON(ts []*downexv1.MediaTrack) []any {
	out := make([]any, len(ts))
	for i, t := range ts {
		out[i] = TrackJSON(t)
	}
	return out
}

// Undefined is what a case's out holds for a function that returned undefined.
var Undefined = map[string]any{"$undefined": true}

// ErrorArg reads an error argument: a tagged {"$error": "ApiError", code, message} is the
// Connect error the native window gets for it; any other tagged error, or a string, is a plain
// error with that text (what toApiError reads as "<code>: <message>").
func ErrorArg(raw json.RawMessage) (error, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return errors.New(text), nil
	}
	var tagged struct {
		Error   string `json:"$error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &tagged); err != nil || tagged.Error == "" {
		return nil, errors.New("formattest: not an error argument: " + string(raw))
	}
	if tagged.Error != "ApiError" {
		return errors.New(tagged.Message), nil
	}
	var code connect.Code
	if err := code.UnmarshalText([]byte(tagged.Code)); err != nil {
		return nil, err
	}
	return connect.NewError(code, errors.New(tagged.Message)), nil
}
