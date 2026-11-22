// Copyright 2026 OpenCloud GmbH <mail@opencloud.eu>
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencloud-eu/reva/v3/pkg/errtypes"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/metadata"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/spaceidindex"
)

// runLockPruner waits a full interval before the first pass so it never competes with the startup rescan
func (t *Tree) runLockPruner(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		t.PruneStaleLocks(context.Background(), interval)
	}
}

// PruneStaleLocks removes, in every space, the lock files older than maxAge that no live node needs any more
func (t *Tree) PruneStaleLocks(ctx context.Context, maxAge time.Duration) {
	spaces := spaceidindex.New(filepath.Join(t.options.Root, lookup.IndexesDir), "by-type")
	spaceTypes, err := spaces.Indexes()
	if err != nil {
		t.log.Warn().Err(err).Msg("lock pruning: could not list the space types")
		return
	}
	before := time.Now().Add(-maxAge)
	for _, spaceType := range spaceTypes {
		spaceIDs, err := spaces.Load(spaceType)
		if err != nil {
			t.log.Warn().Err(err).Str("spaceType", spaceType).Msg("lock pruning: could not load the space index")
			continue
		}
		for spaceID := range spaceIDs {
			root, err := t.idCache.Get(ctx, spaceID, spaceID)
			if err != nil || root == "" {
				t.log.Warn().Err(err).Str("spaceID", spaceID).Msg("lock pruning: the space root is unknown")
				continue
			}
			dir := filepath.Join(root, lookup.MetadataDir, metadata.LocksDir)
			removed, kept, err := t.pruneSpaceLocks(ctx, spaceID, dir, before)
			if err != nil {
				t.log.Warn().Err(err).Str("spaceID", spaceID).Str("dir", dir).Msg("lock pruning: aborted for this space")
			}
			t.log.Info().Str("spaceID", spaceID).Int("removed", removed).Int("kept", kept).Msg("lock pruning: pass finished")
		}
	}
}

// pruneSpaceLocks reads the locks directory in batches, since one space has held half a million entries
func (t *Tree) pruneSpaceLocks(ctx context.Context, spaceID, dir string, before time.Time) (removed, kept int, err error) {
	f, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer f.Close()
	for {
		entries, readErr := f.ReadDir(1024)
		for _, entry := range entries {
			if t.lockIsStale(ctx, spaceID, entry, before) && os.Remove(filepath.Join(dir, entry.Name())) == nil {
				removed++
			} else {
				kept++
			}
		}
		if readErr == io.EOF {
			return removed, kept, nil
		}
		if readErr != nil {
			return removed, kept, readErr
		}
	}
}

// lockIsStale keeps anything younger than maxAge, which is what protects a lock that is held right now
func (t *Tree) lockIsStale(ctx context.Context, spaceID string, entry fs.DirEntry, before time.Time) bool {
	name, isLock := strings.CutSuffix(entry.Name(), ".mlock")
	if !isLock || !entry.Type().IsRegular() {
		return false
	}
	info, err := entry.Info()
	if err != nil || !info.ModTime().Before(before) {
		return false
	}
	// anything but a uuid is the pre-id lock of a path, only ever held for the moment of its assimilation
	if _, err := uuid.Parse(name); err != nil {
		return true
	}
	path, err := t.idCache.Get(ctx, spaceID, name)
	if err != nil {
		var notFound errtypes.NotFound
		return errors.As(err, &notFound)
	}
	_, err = os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}
