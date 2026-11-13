package host

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Settings are a full replacement (UpdateSettings), validated by downexd. A newer downexd
// may have fields this build does not know: the proto messages keep them as unknown
// fields, so the window edits the message GetSettings returned and sends it back, and they
// survive. EditSettings does that in one step; SaveSettings saves a draft the window
// edited for a while (the Settings dialog), copying the unknown fields of the daemon's
// current settings into it, as today's UpdateSettings binding does.

// EditSettings reads the daemon's settings, lets edit change them (an error from edit
// stops before anything is written), and stores the result: a toggle such as the complete
// window's "Don't show this dialog again" or the status bar's speed limit. It returns the
// stored settings. ctx bounds both calls.
func EditSettings(ctx context.Context, d Daemon, edit func(*downexv1.Settings) error) (*downexv1.Settings, error) {
	cur, err := d.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if cur.GetSettings() == nil {
		// Sending an empty message would replace every setting with its default.
		return nil, connect.NewError(connect.CodeInternal, errors.New("downexd answered without its settings"))
	}
	s := proto.CloneOf(cur.GetSettings())
	if err := edit(s); err != nil {
		return nil, err
	}
	resp, err := d.UpdateSettings(ctx, &downexv1.UpdateSettingsRequest{Settings: s})
	if err != nil {
		return nil, err
	}
	return resp.GetSettings(), nil
}

// SaveSettings stores next, a full replacement that the window built from the message
// GetSettings returned earlier, and returns the stored settings. It reads the daemon's
// current settings first and keeps the fields this build does not know from them
// (KeepUnknown), also those added since next was read; when they cannot be read, nothing is
// replaced. next itself is not changed. ctx bounds both calls.
func SaveSettings(ctx context.Context, d Daemon, next *downexv1.Settings) (*downexv1.Settings, error) {
	if next == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no settings to save"))
	}
	next = proto.CloneOf(next)
	cur, err := d.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if s := cur.GetSettings(); s != nil {
		KeepUnknown(next.ProtoReflect(), s.ProtoReflect())
	}
	resp, err := d.UpdateSettings(ctx, &downexv1.UpdateSettingsRequest{Settings: next})
	if err != nil {
		return nil, err
	}
	return resp.GetSettings(), nil
}

// KeepUnknown copies the fields of cur that this build does not know into next: its own,
// those of its singular message fields that both have, and those of the entries of its
// repeated message fields whose string field "id" matches (Settings.site_logins).
func KeepUnknown(next, cur protoreflect.Message) {
	if u := cur.GetUnknown(); len(u) > 0 {
		next.SetUnknown(slices.Clone(u))
	}
	fields := cur.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind || fd.IsMap() {
			continue
		}
		if !cur.Has(fd) || !next.Has(fd) {
			continue
		}
		if !fd.IsList() {
			KeepUnknown(next.Mutable(fd).Message(), cur.Get(fd).Message())
			continue
		}
		id := fd.Message().Fields().ByName("id")
		if id == nil || id.Kind() != protoreflect.StringKind || id.IsList() {
			continue
		}
		byID := map[string]protoreflect.Message{}
		curList := cur.Get(fd).List()
		for j := range curList.Len() {
			m := curList.Get(j).Message()
			if k := m.Get(id).String(); k != "" {
				byID[k] = m
			}
		}
		nextList := next.Mutable(fd).List()
		for j := range nextList.Len() {
			m := nextList.Get(j).Message()
			if old, ok := byID[m.Get(id).String()]; ok {
				KeepUnknown(m, old)
			}
		}
	}
}
