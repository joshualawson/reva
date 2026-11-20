package tree

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hirochachacha/go-smb2/v2/notify"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/watcher"
)

type scanCall struct {
	path   string
	action watcher.EventAction
	isDir  bool
}

type fakeTarget struct {
	mu      sync.Mutex
	scans   []scanCall
	rescans []string
}

func (f *fakeTarget) Scan(path string, action watcher.EventAction, isDir bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans = append(f.scans, scanCall{path, action, isDir})
	return nil
}

func (f *fakeTarget) rescan(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescans = append(f.rescans, path)
}

// fakeSource hands out the queued results, then fails so run returns
type fakeSource struct {
	results []notify.Result
}

func (s *fakeSource) WaitForChange(_ context.Context, _ notify.Filter, recursive bool) (notify.Result, error) {
	if !recursive {
		return notify.Result{}, errors.New("must watch recursively")
	}
	if len(s.results) == 0 {
		return notify.Result{}, errors.New("session lost")
	}
	r := s.results[0]
	s.results = s.results[1:]
	return r, nil
}

func newTestWatcher(t *testing.T, target *fakeTarget, dirs map[string]bool) *SMBNotifyWatcher {
	t.Helper()
	log := zerolog.Nop()
	w, err := NewSMBNotifyWatcher(context.Background(), nil, options.SMBNotifyConfig{
		Address: "nas:445", Share: "homes", Username: "svc",
	}, "/mnt/homes", &log)
	require.NoError(t, err)
	w.target = target
	w.isDir = func(path string) bool { return dirs[path] }
	w.ignored = func(path string) bool { return path == "/mnt/homes/joshua/.oc-nodes" }
	return w
}

func TestSMBNotifyWatcherMapsEvents(t *testing.T) {
	target := &fakeTarget{}
	w := newTestWatcher(t, target, map[string]bool{"/mnt/homes/joshua/Photos": true})

	w.run(&fakeSource{results: []notify.Result{{Events: []notify.Event{
		{Action: notify.Added, Name: `joshua\Photos`},
		{Action: notify.Added, Name: `joshua\Photos\a.jpg`},
		{Action: notify.Modified, Name: `joshua\Photos\a.jpg`},
		{Action: notify.RenamedOldName, Name: `joshua\Photos\a.jpg`},
		{Action: notify.RenamedNewName, Name: `joshua\Photos\b.jpg`},
		{Action: notify.Removed, Name: `joshua\Photos\b.jpg`},
		{Action: notify.AddedStream, Name: `joshua\Photos\b.jpg:stream`},
		{Action: notify.Added, Name: `joshua\.oc-nodes`},
	}}}})

	require.Equal(t, []scanCall{
		{"/mnt/homes/joshua/Photos", watcher.ActionCreate, true},
		{"/mnt/homes/joshua/Photos/a.jpg", watcher.ActionCreate, false},
		{"/mnt/homes/joshua/Photos/a.jpg", watcher.ActionUpdate, false},
		{"/mnt/homes/joshua/Photos/a.jpg", watcher.ActionMoveFrom, false},
		{"/mnt/homes/joshua/Photos/b.jpg", watcher.ActionMove, false},
		{"/mnt/homes/joshua/Photos/b.jpg", watcher.ActionDelete, false},
	}, target.scans)
	require.Empty(t, target.rescans)
}

func TestSMBNotifyWatcherRescansOnOverflow(t *testing.T) {
	target := &fakeTarget{}
	w := newTestWatcher(t, target, nil)

	w.run(&fakeSource{results: []notify.Result{{RescanRequired: true}}})

	require.Equal(t, []string{"/mnt/homes"}, target.rescans)
	require.Empty(t, target.scans)
}

func TestSMBNotifyWatcherReconnectsAndRescans(t *testing.T) {
	target := &fakeTarget{}
	w := newTestWatcher(t, target, nil)
	ctx, cancel := context.WithCancel(context.Background())
	w.ctx = ctx
	w.cfg.ReconnectDelay = 1

	connects := 0
	w.connect = func(context.Context) (changeSource, func(), error) {
		connects++
		switch connects {
		case 1:
			return nil, nil, errors.New("nas down")
		case 2:
			return &fakeSource{results: []notify.Result{{Events: []notify.Event{{Action: notify.Added, Name: "x"}}}}}, func() {}, nil
		default:
			cancel()
			return nil, nil, errors.New("stop")
		}
	}

	w.Watch("")

	require.Equal(t, 3, connects)
	// a fresh session always starts with a rescan: changes during the outage left no notification
	require.Equal(t, []string{"/mnt/homes"}, target.rescans)
	require.Equal(t, []scanCall{{"/mnt/homes/x", watcher.ActionCreate, false}}, target.scans)
}

func TestSMBNotifyWatcherValidatesConfig(t *testing.T) {
	log := zerolog.Nop()
	for _, cfg := range []options.SMBNotifyConfig{
		{Share: "homes", Username: "svc"},
		{Address: "nas", Username: "svc"},
		{Address: "nas", Share: "homes"},
	} {
		_, err := NewSMBNotifyWatcher(context.Background(), nil, cfg, "/mnt/homes", &log)
		require.Error(t, err)
	}
	_, err := NewSMBNotifyWatcher(context.Background(), nil, options.SMBNotifyConfig{Address: "nas", Share: "homes", Username: "svc"}, "", &log)
	require.Error(t, err)
}
