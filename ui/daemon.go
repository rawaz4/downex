package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Daemon is downexd as the window uses it. Every error carries a Connect code
// (connect.CodeOf), so that App can report it as "<code>: <message>".
//
// The window starts downexd (docs/DAEMON-SPEC.md §7) at its first contact with it only:
// the first call, made when the window opens, starts it when nothing listens on its
// endpoint, and the calls made meanwhile wait for that. A later call that finds nothing
// listening fails with unavailable and starts nothing, so that a daemon the user (or an
// installer) stopped stays stopped, and one that cannot start is not started again on
// every status check and events re-subscription. The window's advice holds: start it
// with "downex daemon start" (or use the CLI or the browser, which start it).
type Daemon interface {
	List(ctx context.Context) (*downexv1.ListResponse, error)
	Get(ctx context.Context, id int64) (*downexv1.Download, error)
	Add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.AddResponse, error)
	Pause(ctx context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error)
	Resume(ctx context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error)
	Remove(ctx context.Context, req *downexv1.RemoveRequest) (*downexv1.RemoveResponse, error)
	Confirm(ctx context.Context, req *downexv1.ConfirmRequest) (*downexv1.ConfirmResponse, error)
	AddMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error)
	ProbeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error)
	SetDownloadOptions(ctx context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error)
	PauseAll(ctx context.Context) (*downexv1.PauseAllResponse, error)
	ResumeAll(ctx context.Context) (*downexv1.ResumeAllResponse, error)
	GetSettings(ctx context.Context) (*downexv1.GetSettingsResponse, error)
	UpdateSettings(ctx context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error)
	Status(ctx context.Context) (*downexv1.StatusResponse, error)
	// Watch opens DownloadService.Watch for the downloads ids (none = all). The stream
	// ends when ctx does, or with an error when the daemon goes away (not_found when
	// one of ids names no download).
	Watch(ctx context.Context, ids []int64) (WatchStream, error)

	// M6 (docs/M6-SPEC.md): QueueService, secrets, updates and the post-queue action.
	ListQueues(ctx context.Context) (*downexv1.ListQueuesResponse, error)
	CreateQueue(ctx context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error)
	UpdateQueue(ctx context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error)
	DeleteQueue(ctx context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error)
	StartQueue(ctx context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error)
	StopQueue(ctx context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error)
	MoveDownloads(ctx context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error)
	SetSecret(ctx context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error)
	ClearSecret(ctx context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error)
	CheckForUpdate(ctx context.Context) (*downexv1.CheckForUpdateResponse, error)
	InstallUpdate(ctx context.Context) (*downexv1.InstallUpdateResponse, error)
	CancelPostAction(ctx context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error)

	// Site media (docs/SITE-MEDIA-SPEC.md §3.1): yt-dlp and Deno, which downexd downloads
	// on first use. A daemon older than site media answers unimplemented.
	GetTools(ctx context.Context) (*downexv1.GetToolsResponse, error)
	InstallTools(ctx context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error)
}

// WatchStream is an open DownloadService.Watch stream; *connect.ServerStreamForClient
// implements it.
type WatchStream interface {
	Receive() bool
	Msg() *downexv1.WatchResponse
	Err() error
	Close() error
}

// errElevated replaces client.ErrElevatedStart, whose advice is meant for a command
// prompt, in what the window shows.
var errElevated = errors.New("downexd is not running, and Downex never starts it while it runs as administrator " +
	"(elevated): every program could then use those rights through downexd. Start Downex without administrator " +
	"rights, or run \"downex daemon start\" from a normal prompt")

// errNotRunning is the error of a call that found nothing listening after the first
// contact.
var errNotRunning = fmt.Errorf("%w, and Downex starts it only when its window opens; start it with \"downex daemon start\"",
	ipc.ErrNotRunning)

// clientDaemon reaches downexd through internal/client on the per-user endpoint. The
// first contact checks the endpoint and starts downexd when nothing listens there.
// After it, unary calls go out at once, and Watch checks the endpoint first.
type clientDaemon struct {
	env client.Env

	first chan struct{} // holds a token while a call makes the first contact

	mu        sync.Mutex
	c         *client.Client
	contacted bool  // the first contact is over
	startErr  error // why the first contact could not start downexd
}

func newClientDaemon(env client.Env) *clientDaemon {
	return &clientDaemon{env: env, first: make(chan struct{}, 1)}
}

// client returns the Connect clients of the endpoint, resolving it once.
func (d *clientDaemon) client() (*client.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.c == nil {
		ep, err := d.env.Endpoint()
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("locate the downexd endpoint: %w", err))
		}
		d.c = d.env.Client(ep)
	}
	return d.c, nil
}

// firstContact makes the window's first contact with downexd: it starts downexd unless
// it accepts connections on c's endpoint. Concurrent callers wait for it and share its
// outcome; once it is over, it returns nil at once.
func (d *clientDaemon) firstContact(ctx context.Context, c *client.Client) error {
	if contacted, _ := d.contact(); contacted {
		return nil
	}
	select {
	case d.first <- struct{}{}:
	case <-ctx.Done():
		return connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("downexd is starting and did not answer in time: %w", ctx.Err()))
	}
	defer func() { <-d.first }()
	if contacted, startErr := d.contact(); contacted {
		return startErr // made meanwhile by another call
	}
	err := d.startUnlessRunning(ctx, c)
	d.mu.Lock()
	d.contacted, d.startErr = true, err
	d.mu.Unlock()
	return err
}

// contact reports whether the first contact is over, and why it could not start downexd.
func (d *clientDaemon) contact() (contacted bool, startErr error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.contacted, d.startErr
}

func (d *clientDaemon) startUnlessRunning(ctx context.Context, c *client.Client) error {
	up, err := d.env.Running(ctx, c.Endpoint)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if up {
		return nil
	}
	if _, err := d.env.Start(ctx, c); err != nil {
		if errors.Is(err, client.ErrElevatedStart) {
			return connect.NewError(connect.CodeFailedPrecondition, errElevated)
		}
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return nil
}

// notRunning is the error of a call that found nothing listening after the first
// contact; it repeats why that contact could not start downexd.
func (d *clientDaemon) notRunning() error {
	_, startErr := d.contact()
	if startErr == nil {
		return connect.NewError(connect.CodeUnavailable, errNotRunning)
	}
	why := startErr.Error()
	var ce *connect.Error
	if errors.As(startErr, &ce) {
		why = ce.Message() // without its code
	}
	return connect.NewError(connect.CodeUnavailable,
		fmt.Errorf("%w (starting it when the window opened failed: %s)", errNotRunning, why))
}

// rpc is a unary method of a Connect client.
type rpc[Req, Res any] = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)

// unary calls method with req, after the first contact.
func unary[Req, Res any](ctx context.Context, d *clientDaemon, req *Req,
	method func(*client.Client) rpc[Req, Res],
) (*Res, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	if err := d.firstContact(ctx, c); err != nil {
		return nil, err
	}
	resp, err := method(c)(ctx, connect.NewRequest(req))
	if err != nil {
		if errors.Is(err, ipc.ErrNotRunning) {
			return nil, d.notRunning()
		}
		return nil, err
	}
	return resp.Msg, nil
}

func (d *clientDaemon) List(ctx context.Context) (*downexv1.ListResponse, error) {
	return unary(ctx, d, &downexv1.ListRequest{}, func(c *client.Client) rpc[downexv1.ListRequest, downexv1.ListResponse] {
		return c.Downloads.List
	})
}

func (d *clientDaemon) Get(ctx context.Context, id int64) (*downexv1.Download, error) {
	resp, err := unary(ctx, d, &downexv1.GetRequest{Id: id}, func(c *client.Client) rpc[downexv1.GetRequest, downexv1.GetResponse] {
		return c.Downloads.Get
	})
	if err != nil {
		return nil, err
	}
	return resp.GetDownload(), nil
}

func (d *clientDaemon) Add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.AddResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.AddRequest, downexv1.AddResponse] {
		return c.Downloads.Add
	})
}

func (d *clientDaemon) Pause(ctx context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.PauseRequest, downexv1.PauseResponse] {
		return c.Downloads.Pause
	})
}

func (d *clientDaemon) Resume(ctx context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.ResumeRequest, downexv1.ResumeResponse] {
		return c.Downloads.Resume
	})
}

func (d *clientDaemon) Remove(ctx context.Context, req *downexv1.RemoveRequest) (*downexv1.RemoveResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.RemoveRequest, downexv1.RemoveResponse] {
		return c.Downloads.Remove
	})
}

func (d *clientDaemon) Confirm(ctx context.Context, req *downexv1.ConfirmRequest) (*downexv1.ConfirmResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.ConfirmRequest, downexv1.ConfirmResponse] {
		return c.Downloads.Confirm
	})
}

func (d *clientDaemon) AddMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.AddMediaRequest, downexv1.AddMediaResponse] {
		return c.Downloads.AddMedia
	})
}

func (d *clientDaemon) ProbeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.ProbeMediaRequest, downexv1.ProbeMediaResponse] {
		return c.Downloads.ProbeMedia
	})
}

func (d *clientDaemon) SetDownloadOptions(ctx context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.SetDownloadOptionsRequest, downexv1.SetDownloadOptionsResponse] {
		return c.Downloads.SetDownloadOptions
	})
}

func (d *clientDaemon) PauseAll(ctx context.Context) (*downexv1.PauseAllResponse, error) {
	return unary(ctx, d, &downexv1.PauseAllRequest{}, func(c *client.Client) rpc[downexv1.PauseAllRequest, downexv1.PauseAllResponse] {
		return c.Downloads.PauseAll
	})
}

func (d *clientDaemon) ResumeAll(ctx context.Context) (*downexv1.ResumeAllResponse, error) {
	return unary(ctx, d, &downexv1.ResumeAllRequest{}, func(c *client.Client) rpc[downexv1.ResumeAllRequest, downexv1.ResumeAllResponse] {
		return c.Downloads.ResumeAll
	})
}

func (d *clientDaemon) GetSettings(ctx context.Context) (*downexv1.GetSettingsResponse, error) {
	return unary(ctx, d, &downexv1.GetSettingsRequest{}, func(c *client.Client) rpc[downexv1.GetSettingsRequest, downexv1.GetSettingsResponse] {
		return c.Daemon.GetSettings
	})
}

func (d *clientDaemon) UpdateSettings(ctx context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.UpdateSettingsRequest, downexv1.UpdateSettingsResponse] {
		return c.Daemon.UpdateSettings
	})
}

func (d *clientDaemon) Status(ctx context.Context) (*downexv1.StatusResponse, error) {
	return unary(ctx, d, &downexv1.StatusRequest{}, func(c *client.Client) rpc[downexv1.StatusRequest, downexv1.StatusResponse] {
		return c.Daemon.Status
	})
}

// Watch checks the endpoint before it subscribes: a Connect stream reports a failed
// connection only from Receive, after the call has returned.
func (d *clientDaemon) Watch(ctx context.Context, ids []int64) (WatchStream, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	if err := d.firstContact(ctx, c); err != nil {
		return nil, err
	}
	if up, err := d.env.Running(ctx, c.Endpoint); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	} else if !up {
		return nil, d.notRunning()
	}
	s, err := c.Downloads.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: ids}))
	if err != nil {
		return nil, err
	}
	return s, nil
}
