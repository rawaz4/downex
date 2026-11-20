package daemon

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// DefaultReadMaxBytes bounds request messages: an AddRequest carries at most a 1 MiB
// body, 64 KiB of headers and a 32 KiB URL.
const DefaultReadMaxBytes = 2 << 20

// NewHandler serves the Connect services of m (downloads, daemon, queues). onShutdown,
// if non-nil, is called once after a Shutdown RPC has paused every download, so that
// the process can stop serving and exit. opts are appended to
// connect.WithReadMaxBytes(DefaultReadMaxBytes).
func NewHandler(m *Manager, onShutdown func(), opts ...connect.HandlerOption) http.Handler {
	opts = append([]connect.HandlerOption{connect.WithReadMaxBytes(DefaultReadMaxBytes)}, opts...)
	mux := http.NewServeMux()
	mux.Handle(downexv1connect.NewDownloadServiceHandler(NewDownloadService(m), opts...))
	mux.Handle(downexv1connect.NewDaemonServiceHandler(NewDaemonService(m, onShutdown), opts...))
	mux.Handle(downexv1connect.NewQueueServiceHandler(NewQueueService(m), opts...))
	return mux
}

// NewDownloadService returns the DownloadService implementation backed by m.
func NewDownloadService(m *Manager) downexv1connect.DownloadServiceHandler {
	return downloadService{m: m}
}

// NewDaemonService returns the DaemonService implementation backed by m; see NewHandler
// for onShutdown.
func NewDaemonService(m *Manager, onShutdown func()) downexv1connect.DaemonServiceHandler {
	if onShutdown == nil {
		onShutdown = func() {}
	}
	return daemonService{m: m, onShutdown: sync.OnceFunc(onShutdown)}
}

type downloadService struct{ m *Manager }

var _ downexv1connect.DownloadServiceHandler = downloadService{}

func (s downloadService) Add(ctx context.Context, req *connect.Request[downexv1.AddRequest]) (*connect.Response[downexv1.AddResponse], error) {
	d, err := s.m.Add(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.AddResponse{Download: d}), nil
}

func (s downloadService) List(ctx context.Context, _ *connect.Request[downexv1.ListRequest]) (*connect.Response[downexv1.ListResponse], error) {
	all, err := s.m.List(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ListResponse{Downloads: all}), nil
}

// Rescue adopts a partial file a browser left unfinished (rescue.go).
func (s downloadService) Rescue(ctx context.Context, req *connect.Request[downexv1.RescueRequest]) (*connect.Response[downexv1.RescueResponse], error) {
	d, kept, err := s.m.Rescue(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.RescueResponse{Download: d, RescuedBytes: kept}), nil
}

func (s downloadService) Get(ctx context.Context, req *connect.Request[downexv1.GetRequest]) (*connect.Response[downexv1.GetResponse], error) {
	d, err := s.m.Get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.GetResponse{Download: d}), nil
}

func (s downloadService) Pause(ctx context.Context, req *connect.Request[downexv1.PauseRequest]) (*connect.Response[downexv1.PauseResponse], error) {
	d, err := s.m.Pause(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.PauseResponse{Download: d}), nil
}

func (s downloadService) Resume(ctx context.Context, req *connect.Request[downexv1.ResumeRequest]) (*connect.Response[downexv1.ResumeResponse], error) {
	d, err := s.m.Resume(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ResumeResponse{Download: d}), nil
}

func (s downloadService) Remove(ctx context.Context, req *connect.Request[downexv1.RemoveRequest]) (*connect.Response[downexv1.RemoveResponse], error) {
	if err := s.m.Remove(ctx, req.Msg.GetId(), req.Msg.GetDeleteFile()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.RemoveResponse{}), nil
}

func (s downloadService) RenewLink(ctx context.Context, req *connect.Request[downexv1.RenewLinkRequest]) (*connect.Response[downexv1.RenewLinkResponse], error) {
	resp, err := s.m.RenewLink(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s downloadService) Watch(ctx context.Context, req *connect.Request[downexv1.WatchRequest], stream *connect.ServerStream[downexv1.WatchResponse]) error {
	return s.m.Watch(ctx, req.Msg.GetIds(), stream.Send)
}

func (s downloadService) Confirm(ctx context.Context, req *connect.Request[downexv1.ConfirmRequest]) (*connect.Response[downexv1.ConfirmResponse], error) {
	d, err := s.m.Confirm(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ConfirmResponse{Download: d}), nil
}

func (s downloadService) PauseAll(ctx context.Context, _ *connect.Request[downexv1.PauseAllRequest]) (*connect.Response[downexv1.PauseAllResponse], error) {
	n, err := s.m.PauseAll(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.PauseAllResponse{Paused: int32(n)}), nil
}

func (s downloadService) ResumeAll(ctx context.Context, _ *connect.Request[downexv1.ResumeAllRequest]) (*connect.Response[downexv1.ResumeAllResponse], error) {
	n, err := s.m.ResumeAll(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ResumeAllResponse{Resumed: int32(n)}), nil
}

func (s downloadService) AddMedia(ctx context.Context, req *connect.Request[downexv1.AddMediaRequest]) (*connect.Response[downexv1.AddMediaResponse], error) {
	d, ticket, err := s.m.addMedia(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.AddMediaResponse{Download: d, FetchTicket: ticket}), nil
}

func (s downloadService) ProbeMedia(ctx context.Context, req *connect.Request[downexv1.ProbeMediaRequest]) (*connect.Response[downexv1.ProbeMediaResponse], error) {
	info, err := s.m.ProbeMedia(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ProbeMediaResponse{Media: info}), nil
}

func (s downloadService) SetDownloadOptions(ctx context.Context, req *connect.Request[downexv1.SetDownloadOptionsRequest]) (*connect.Response[downexv1.SetDownloadOptionsResponse], error) {
	d, err := s.m.SetDownloadOptions(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.SetDownloadOptionsResponse{Download: d}), nil
}

type daemonService struct {
	m          *Manager
	onShutdown func()
}

var _ downexv1connect.DaemonServiceHandler = daemonService{}

func (s daemonService) Status(context.Context, *connect.Request[downexv1.StatusRequest]) (*connect.Response[downexv1.StatusResponse], error) {
	return connect.NewResponse(s.m.Status()), nil
}

func (s daemonService) GetSettings(context.Context, *connect.Request[downexv1.GetSettingsRequest]) (*connect.Response[downexv1.GetSettingsResponse], error) {
	return connect.NewResponse(&downexv1.GetSettingsResponse{Settings: s.m.protoSettings(s.m.Settings())}), nil
}

func (s daemonService) UpdateSettings(ctx context.Context, req *connect.Request[downexv1.UpdateSettingsRequest]) (*connect.Response[downexv1.UpdateSettingsResponse], error) {
	next, err := s.m.UpdateSettings(ctx, req.Msg.GetSettings())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.UpdateSettingsResponse{Settings: s.m.protoSettings(next)}), nil
}

// Shutdown pauses every download (progress committed) before it answers, then asks
// the process to exit. The pause is not cut short when the client goes away. A pause
// that does not finish in time is reported as DeadlineExceeded; the daemon exits anyway
// and the next start requeues those downloads.
func (s daemonService) Shutdown(ctx context.Context, _ *connect.Request[downexv1.ShutdownRequest]) (*connect.Response[downexv1.ShutdownResponse], error) {
	err := s.m.Shutdown(context.WithoutCancel(ctx))
	s.onShutdown()
	if err != nil {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewResponse(&downexv1.ShutdownResponse{}), nil
}
