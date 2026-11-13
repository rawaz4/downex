package media_test

import (
	"encoding/json"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
	"downex/ui/core/media"
)

// TestGoldens replays every media.ts case of the 0.9.2 formatter goldens (the inputs of the
// window's vitest and component tests, with Blink's output) in each locale and time zone,
// language names from ICU as the window's Intl.DisplayNames gives them.
func TestGoldens(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	media.SetLanguageNamer(locale.LanguageName)
	t.Cleanup(func() { media.SetLanguageNamer(nil) })
	RunGoldens(t, g, "media.", mediaCalls())
}

func arg(args []json.RawMessage, i int) json.RawMessage {
	if i < len(args) {
		return args[i]
	}
	return json.RawMessage(`{"$undefined":true}`)
}

// truthy is JavaScript's truthiness of an optional argument (true, "site").
func truthy(raw json.RawMessage) bool {
	if IsUndefined(raw) {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case nil:
		return false
	}
	return true
}

func str(raw json.RawMessage) string {
	s, _ := Str(raw)
	return s
}

func pickArg(raw json.RawMessage) (media.TrackPick, error) {
	var p struct{ Video, Audio string }
	err := json.Unmarshal(raw, &p)
	return media.TrackPick{Video: p.Video, Audio: p.Audio}, err
}

func pickJSON(p media.TrackPick) map[string]any {
	return map[string]any{"video": p.Video, "audio": p.Audio}
}

var sizeKinds = map[string]media.SizeKind{"unknown": media.SizeUnknown, "exact": media.SizeExact, "approx": media.SizeApprox, "estimate": media.SizeEstimate}

func sizeKindName(k media.SizeKind) string {
	for name, v := range sizeKinds {
		if v == k {
			return name
		}
	}
	return "?"
}

func info(f func(m *downexv1.MediaInfo, args []json.RawMessage) (any, error)) Call {
	return func(args []json.RawMessage) (any, error) {
		m, err := MediaInfoArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(m, args[1:])
	}
}

func infoPick(f func(m *downexv1.MediaInfo, p media.TrackPick, args []json.RawMessage) any) Call {
	return info(func(m *downexv1.MediaInfo, args []json.RawMessage) (any, error) {
		p, err := pickArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(m, p, args[1:]), nil
	})
}

func track(f func(t *downexv1.MediaTrack, args []json.RawMessage) any) Call {
	return func(args []json.RawMessage) (any, error) {
		tr, err := TrackArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(tr, args[1:]), nil
	}
}

func text(f func(s string, args []json.RawMessage) any) Call {
	return func(args []json.RawMessage) (any, error) {
		s, err := Str(args[0])
		if err != nil {
			return nil, err
		}
		return f(s, args[1:]), nil
	}
}

func failure(f func(err error, args []json.RawMessage) any) Call {
	return func(args []json.RawMessage) (any, error) {
		e, err := ErrorArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(e, args[1:]), nil
	}
}

func mediaCalls() map[string]Call {
	return map[string]Call{
		"media.audioOnlyOffered": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) {
			return media.AudioOnlyOffered(m), nil
		}),
		"media.audioTrackLabel": track(func(tr *downexv1.MediaTrack, a []json.RawMessage) any {
			return media.AudioTrackLabel(tr, truthy(arg(a, 0)))
		}),
		"media.audioTrackTitle": track(func(tr *downexv1.MediaTrack, a []json.RawMessage) any {
			return media.AudioTrackTitle(tr, truthy(arg(a, 0)))
		}),
		"media.audioTracks": func(args []json.RawMessage) (any, error) {
			ts, err := TracksArg(args[0])
			if err != nil {
				return nil, err
			}
			return TracksJSON(media.AudioTracks(ts, truthy(arg(args, 1)))), nil
		},
		"media.cannotRetry": text(func(s string, _ []json.RawMessage) any { return media.CannotRetry(s) }),
		"media.chosenTracks": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) {
			v, a := media.ChosenTracks(m)
			return map[string]any{"video": v, "audio": a}, nil
		}),
		"media.codecName":  text(func(s string, _ []json.RawMessage) any { return media.CodecName(s) }),
		"media.codecNames": text(func(s string, _ []json.RawMessage) any { return media.CodecNames(s) }),
		"media.codecSummary": info(func(m *downexv1.MediaInfo, a []json.RawMessage) (any, error) {
			if IsUndefined(arg(a, 0)) {
				return media.CodecSummary(m, nil), nil
			}
			p, err := pickArg(a[0])
			if err != nil {
				return nil, err
			}
			return media.CodecSummary(m, &p), nil
		}),
		"media.containerLabel": text(func(s string, _ []json.RawMessage) any { return media.ContainerLabel(s) }),
		"media.defaultPick": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) {
			return pickJSON(media.DefaultPick(m)), nil
		}),
		"media.estimatedBytes": infoPick(func(m *downexv1.MediaInfo, p media.TrackPick, _ []json.RawMessage) any {
			return media.EstimatedBytes(m, p)
		}),
		"media.formatLabel": text(func(s string, _ []json.RawMessage) any { return media.FormatLabel(s) }),
		"media.isMedia": func(args []json.RawMessage) (any, error) {
			d, err := DownloadArg(args[0])
			if err != nil {
				return nil, err
			}
			return media.IsMedia(d), nil
		},
		"media.isSite": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) { return media.IsSite(m), nil }),
		"media.isSiteDownload": func(args []json.RawMessage) (any, error) {
			d, err := DownloadArg(args[0])
			if err != nil {
				return nil, err
			}
			return media.IsSiteDownload(d), nil
		},
		"media.isVideoTrack":      track(func(tr *downexv1.MediaTrack, _ []json.RawMessage) any { return media.IsVideoTrack(tr) }),
		"media.languageName":      text(func(s string, _ []json.RawMessage) any { return media.LanguageName(s) }),
		"media.looksLikeManifest": text(func(s string, _ []json.RawMessage) any { return media.LooksLikeManifest(s) }),
		"media.mediaErrorText": failure(func(e error, a []json.RawMessage) any {
			return media.MediaErrorText(e, str(arg(a, 0)) == "site")
		}),
		"media.mediaRefusal": failure(func(e error, _ []json.RawMessage) any {
			switch media.MediaRefusal(e) {
			case media.Encrypted:
				return "encrypted"
			case media.Live:
				return "live"
			}
			return nil
		}),
		"media.needsAudio": info(func(m *downexv1.MediaInfo, a []json.RawMessage) (any, error) {
			return media.NeedsAudio(m, str(a[0])), nil
		}),
		"media.needsToolsSettings": failure(func(e error, _ []json.RawMessage) any { return media.NeedsToolsSettings(e) }),
		"media.pickSize": infoPick(func(m *downexv1.MediaInfo, p media.TrackPick, _ []json.RawMessage) any {
			s := media.PickSizeOf(m, p)
			return map[string]any{"bytes": s.Bytes, "kind": sizeKindName(s.Kind)}
		}),
		"media.pickSizeText": func(args []json.RawMessage) (any, error) {
			var p struct {
				Bytes int64  `json:"bytes"`
				Kind  string `json:"kind"`
			}
			if err := json.Unmarshal(args[0], &p); err != nil {
				return nil, err
			}
			return media.PickSizeText(media.PickSize{Bytes: p.Bytes, Kind: sizeKinds[p.Kind]}), nil
		},
		"media.pickedAudio": infoPick(func(m *downexv1.MediaInfo, p media.TrackPick, _ []json.RawMessage) any {
			return media.PickedAudio(m, p)
		}),
		"media.qualityName":   track(func(tr *downexv1.MediaTrack, _ []json.RawMessage) any { return media.QualityName(tr) }),
		"media.siteErrorCode": failure(func(e error, _ []json.RawMessage) any { return media.SiteErrorCode(e) }),
		"media.siteErrorHint": text(func(s string, _ []json.RawMessage) any { return media.SiteErrorHint(s) }),
		"media.siteErrorText": failure(func(e error, _ []json.RawMessage) any { return media.SiteErrorText(e) }),
		"media.siteQualityLabel": infoPick(func(m *downexv1.MediaInfo, p media.TrackPick, a []json.RawMessage) any {
			tr, err := TrackArg(a[0])
			if err != nil {
				return err.Error()
			}
			return media.SiteQualityLabel(m, p, tr)
		}),
		"media.siteQualityText": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) {
			return media.SiteQualityText(m), nil
		}),
		"media.siteRetry": failure(func(e error, _ []json.RawMessage) any {
			r := media.SiteRetryOf(e)
			switch r.Kind {
			case media.ToolsInstalling:
				return map[string]any{"kind": r.Kind.String(), "fraction": r.Fraction}
			case media.SiteResolving:
				return map[string]any{"kind": r.Kind.String()}
			}
			return nil
		}),
		"media.siteVideoLabel": info(func(m *downexv1.MediaInfo, _ []json.RawMessage) (any, error) {
			return media.SiteVideoLabel(m), nil
		}),
		"media.splitCode": text(func(s string, _ []json.RawMessage) any {
			code, rest := media.SplitCode(s)
			return map[string]any{"code": code, "rest": rest}
		}),
		"media.trackById": info(func(m *downexv1.MediaInfo, a []json.RawMessage) (any, error) {
			return TrackJSON(media.TrackByID(m, str(a[0]))), nil
		}),
		"media.trackSizeText": track(func(tr *downexv1.MediaTrack, _ []json.RawMessage) any { return media.TrackSizeText(tr) }),
		"media.videoPictureText": track(func(tr *downexv1.MediaTrack, a []json.RawMessage) any {
			return media.VideoPictureText(tr, truthy(arg(a, 0)))
		}),
		"media.videoTrackLabel": track(func(tr *downexv1.MediaTrack, a []json.RawMessage) any {
			return media.VideoTrackLabel(tr, truthy(arg(a, 0)))
		}),
		"media.videoTracks": func(args []json.RawMessage) (any, error) {
			ts, err := TracksArg(args[0])
			if err != nil {
				return nil, err
			}
			keep := ""
			if k := arg(args, 2); !IsUndefined(k) {
				keep = str(k)
			}
			return TracksJSON(media.VideoTracks(ts, truthy(arg(args, 1)), keep)), nil
		},
		"media.withVideo": infoPick(func(m *downexv1.MediaInfo, p media.TrackPick, a []json.RawMessage) any {
			return pickJSON(media.WithVideo(m, p, str(a[0])))
		}),
	}
}
