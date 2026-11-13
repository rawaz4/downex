package daemon

import (
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// maxSecretIDBytes bounds the id of a secret reference (site login ids are 16 hex
// digits; the keyring's names are limited too).
const maxSecretIDBytes = 64

// maxPasswordBytes bounds a password (the keyring accepts at most 64 KiB).
const maxPasswordBytes = 4 << 10

// SetSecret stores a proxy or site-login password in the keyring of Config.Network (the
// OS's protected storage). A site login must exist in the settings (its id is assigned
// by UpdateSettings). The password is never returned, logged or echoed in Settings; only
// has_password turns true, and settings notices tell clients to read it again.
func (s daemonService) SetSecret(_ context.Context, req *connect.Request[downexv1.SetSecretRequest]) (*connect.Response[downexv1.SetSecretResponse], error) {
	rt := s.m.cfg.Network
	if rt == nil || rt.keyring == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("secret storage is not configured"))
	}
	if s.m.isClosed() {
		return nil, errShuttingDown()
	}
	kind, id := req.Msg.GetKind(), req.Msg.GetId()
	if err := validateSecretRef(kind, id); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	pw := req.Msg.GetPassword()
	switch {
	case pw == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("password must not be empty"))
	case len(pw) > maxPasswordBytes:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("password is too long"))
	}
	if kind == SecretKindSiteLogin {
		logins := s.m.Settings().Network.SiteLogins
		if !slices.ContainsFunc(logins, func(l SiteLogin) bool { return l.ID == id }) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no site login has this id; save the settings first"))
		}
	}
	if err := rt.keyring.Set(secretName(kind, id), []byte(pw)); err != nil {
		s.m.log.Warn("cannot store a password", "kind", kind, "id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("cannot store the secret"))
	}
	s.m.log.Info("password stored", "kind", kind, "id", id)
	s.announceSecretChange()
	return connect.NewResponse(&downexv1.SetSecretResponse{}), nil
}

// ClearSecret removes a stored password. Removing a missing secret (or one of a site
// login that no longer exists) is not an error.
func (s daemonService) ClearSecret(_ context.Context, req *connect.Request[downexv1.ClearSecretRequest]) (*connect.Response[downexv1.ClearSecretResponse], error) {
	rt := s.m.cfg.Network
	if rt == nil || rt.keyring == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("secret storage is not configured"))
	}
	if s.m.isClosed() {
		return nil, errShuttingDown()
	}
	kind, id := req.Msg.GetKind(), req.Msg.GetId()
	if err := validateSecretRef(kind, id); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := rt.keyring.Delete(secretName(kind, id)); err != nil {
		s.m.log.Warn("cannot remove a password", "kind", kind, "id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("cannot clear the secret"))
	}
	s.announceSecretChange()
	return connect.NewResponse(&downexv1.ClearSecretResponse{}), nil
}

// announceSecretChange publishes a settings notice so clients re-read has_password.
func (s daemonService) announceSecretChange() {
	s.m.notify(Notice{Kind: NoticeSettings, Settings: s.m.Settings()})
}

// validateSecretRef checks the kind and id of a secret reference.
func validateSecretRef(kind, id string) error {
	switch kind {
	case SecretKindProxy:
		if id != "" {
			return errors.New(`proxy secret must have an empty id`)
		}
	case SecretKindSiteLogin:
		switch {
		case id == "":
			return errors.New("site_login secret needs an id")
		case len(id) > maxSecretIDBytes || !utf8.ValidString(id):
			return errors.New("site_login id is not valid")
		}
		for _, c := range []byte(id) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return errors.New("site_login id is not valid")
			}
		}
	default:
		return errors.New(`kind must be "proxy" or "site_login"`)
	}
	return nil
}
