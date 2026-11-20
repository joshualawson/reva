package tree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	smb2 "github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
	"github.com/hirochachacha/go-smb2/v2/notify"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/watcher"
)

// what the SMB server reports: names, sizes and content writes; attribute and access-time noise is left out
const smbNotifyFilter = notify.FileName | notify.DirName | notify.Size | notify.LastWrite | notify.Creation

var (
	smbNotifyEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reva_fs_smbnotify_events_total",
		Help: "Change notifications received from the SMB server, by action",
	}, []string{"action"})
	smbNotifyRescans = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reva_fs_smbnotify_rescans_total",
		Help: "Full rescans of the watch root because the server could not keep up",
	})
	smbNotifyReconnects = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reva_fs_smbnotify_reconnects_total",
		Help: "Reconnects of the SMB notification session",
	})
)

// changeSource delivers one batch of change notifications per call; the SMB directory handle in production
type changeSource interface {
	WaitForChange(ctx context.Context, filter notify.Filter, recursive bool) (notify.Result, error)
}

// smbScanTarget is the part of the tree the watcher drives
type smbScanTarget interface {
	Scan(path string, action watcher.EventAction, isDir bool) error
	rescan(path string)
}

// SMBNotifyWatcher learns about changes from the SMB server that serves the same tree, so edits made by SMB clients show up
type SMBNotifyWatcher struct {
	ctx       context.Context
	target    smbScanTarget
	cfg       options.SMBNotifyConfig
	watchRoot string
	log       *zerolog.Logger

	// seams for tests
	connect func(ctx context.Context) (changeSource, func(), error)
	isDir   func(path string) bool
	ignored func(path string) bool
}

// NewSMBNotifyWatcher validates the configuration; the connection is made in Watch so a NAS that is down delays, not fails, startup
func NewSMBNotifyWatcher(ctx context.Context, tree *Tree, cfg options.SMBNotifyConfig, watchRoot string, log *zerolog.Logger) (*SMBNotifyWatcher, error) {
	switch {
	case cfg.Address == "":
		return nil, errors.New("smbnotify: address is required")
	case cfg.Share == "":
		return nil, errors.New("smbnotify: share is required")
	case cfg.Username == "":
		return nil, errors.New("smbnotify: username is required")
	case watchRoot == "":
		return nil, errors.New("smbnotify: watch_root is required (the local path of the share directory)")
	}
	w := &SMBNotifyWatcher{
		ctx:       ctx,
		target:    tree,
		cfg:       cfg,
		watchRoot: filepath.Clean(watchRoot),
		log:       log,
		isDir:     localIsDir,
	}
	w.connect = w.dial
	if tree != nil && tree.Ignorer != nil {
		w.ignored = tree.Ignorer.IsIgnored
	}
	return w, nil
}

// Watch keeps a notification session open for the lifetime of the tree, reconnecting when it drops
func (w *SMBNotifyWatcher) Watch(_ string) {
	delay := w.cfg.ReconnectDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	first := true
	for {
		if w.ctx.Err() != nil {
			return
		}
		if !first {
			smbNotifyReconnects.Inc()
		}
		first = false

		src, closeSource, err := w.connect(w.ctx)
		if err != nil {
			w.log.Warn().Err(err).Str("address", w.cfg.Address).Str("share", w.cfg.Share).Dur("retry_in", delay).Msg("smbnotify: could not connect")
			w.sleep(delay)
			continue
		}
		w.log.Info().Str("address", w.cfg.Address).Str("share", w.cfg.Share).Str("path", w.cfg.Path).Str("root", w.watchRoot).Msg("smbnotify: watching")

		// whatever changed while no session was open is only found by a rescan
		w.rescan()
		w.run(src)
		closeSource()
		w.sleep(delay)
	}
}

// run consumes notifications until the source fails
func (w *SMBNotifyWatcher) run(src changeSource) {
	for {
		res, err := src.WaitForChange(w.ctx, smbNotifyFilter, true)
		if err != nil {
			if w.ctx.Err() == nil {
				w.log.Warn().Err(err).Msg("smbnotify: notification session lost")
			}
			return
		}
		if res.RescanRequired {
			w.rescan()
			continue
		}
		for _, ev := range res.Events {
			w.handle(ev)
		}
	}
}

// handle maps one SMB change to a scan; events arrive in order, and the rename pair relies on that order
func (w *SMBNotifyWatcher) handle(ev notify.Event) {
	path := filepath.Join(w.watchRoot, strings.ReplaceAll(ev.Name, `\`, "/"))
	if w.ignored != nil && w.ignored(path) {
		return
	}

	var action watcher.EventAction
	label := ""
	switch ev.Action {
	case notify.Added:
		action, label = watcher.ActionCreate, "create"
	case notify.Modified:
		action, label = watcher.ActionUpdate, "update"
	case notify.Removed, notify.RemovedByDelete:
		action, label = watcher.ActionDelete, "delete"
	case notify.RenamedOldName:
		action, label = watcher.ActionMoveFrom, "move_from"
	case notify.RenamedNewName:
		action, label = watcher.ActionMove, "move"
	default:
		w.log.Trace().Uint32("action", uint32(ev.Action)).Str("name", ev.Name).Msg("smbnotify: unhandled event")
		return
	}
	smbNotifyEvents.WithLabelValues(label).Inc()

	// a gone item cannot be stat'ed; Scan only uses the flag for metrics in that case
	isDir := false
	if action != watcher.ActionDelete && action != watcher.ActionMoveFrom {
		isDir = w.isDir(path)
	}
	if err := w.target.Scan(path, action, isDir); err != nil {
		w.log.Error().Err(err).Str("path", path).Str("action", label).Msg("smbnotify: scan failed")
	}
}

func (w *SMBNotifyWatcher) rescan() {
	smbNotifyRescans.Inc()
	w.log.Info().Str("root", w.watchRoot).Msg("smbnotify: rescanning the watch root")
	w.target.rescan(w.watchRoot)
}

func (w *SMBNotifyWatcher) sleep(d time.Duration) {
	select {
	case <-w.ctx.Done():
	case <-time.After(d):
	}
}

// dial opens the session, share and the watched directory; the returned function tears all three down
func (w *SMBNotifyWatcher) dial(ctx context.Context) (changeSource, func(), error) {
	cred := auth.NTLMCredential{User: w.cfg.Username, Password: w.cfg.Password}
	if w.cfg.Domain != "" {
		domain := w.cfg.Domain
		cred.Domain = &domain
	}
	session, err := (&smb2.Dialer{Credentials: cred}).Dial(ctx, w.cfg.Address)
	if err != nil {
		return nil, nil, errors.Wrap(err, "dial")
	}
	share, err := session.Mount(ctx, w.cfg.Share)
	if err != nil {
		_ = session.Close()
		return nil, nil, errors.Wrap(err, "mount share")
	}
	dir, err := share.Open(ctx, w.cfg.Path)
	if err != nil {
		_ = share.Unmount(ctx)
		_ = session.Close()
		return nil, nil, errors.Wrap(err, "open watched directory")
	}
	closeAll := func() {
		_ = dir.Close(context.Background())
		_ = share.Unmount(context.Background())
		_ = session.Close()
	}
	return dir, closeAll, nil
}

func localIsDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

// rescan assimilates the whole subtree; used when notifications were lost
func (t *Tree) rescan(path string) {
	t.scanDebouncer.Debounce(scanItem{Path: path, Recurse: true})
}
