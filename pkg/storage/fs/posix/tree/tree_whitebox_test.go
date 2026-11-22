// Copyright 2026 OpenCloud GmbH <mail@opencloud.eu>
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/ignore"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"

	"github.com/opencloud-eu/reva/v3/pkg/errtypes"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/tree/mocks"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/watcher"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/node"
	decomposedoptions "github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/options"
)

// fakePathLookup is a node.PathLookup that only knows how to answer
// InternalPath. Any other method panics, which keeps the spec honest about
// the code path it exercises.
type fakePathLookup struct {
	node.PathLookup
	internalPath string
}

func (f fakePathLookup) InternalPath(_, _ string) string { return f.internalPath }

var _ = Describe("Tree", func() {
	Describe("Scan", func() {
		It("drops events below an ignored directory before queueing them", func() {
			dir := GinkgoT().TempDir()
			logger := zerolog.Nop()
			// nothing else is wired: an event that gets past the guard would hit the nil debouncer
			t := &Tree{log: &logger, options: &options.Options{Options: decomposedoptions.Options{Root: dir}, IgnoredDirPatterns: []string{".*"}}}
			t.Ignorer = ignore.NewIgnorer(t.options, t.log)
			trash := filepath.Join(dir, "users", "u", ".Trash-1000")
			Expect(os.MkdirAll(trash, 0700)).To(Succeed())

			Expect(t.Scan(filepath.Join(trash, "junk.txt"), watcher.ActionCreate, false)).To(Succeed())
			Expect(t.Scan(trash, watcher.ActionCreate, true)).To(Succeed())
			Expect(t.Scan(filepath.Join(dir, "users", "u", ".gone"), watcher.ActionDelete, true)).To(Succeed())
		})
	})

	Describe("ListFolder", func() {
		var (
			ctx        context.Context
			resolver   *mocks.IDResolver
			t          *Tree
			folder     *node.Node
			assimilate bool
			// the error returned by the assimilation seam, configurable per spec
			assimilateErr error
		)

		BeforeEach(func() {
			ctx = context.Background()
			resolver = mocks.NewIDResolver(GinkgoT())

			dir, err := os.MkdirTemp("", "listfolder-*")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(dir) })
			Expect(os.WriteFile(filepath.Join(dir, "entry.txt"), []byte("data"), 0600)).To(Succeed())

			assimilate = false
			assimilateErr = nil

			logger := zerolog.Nop()
			t = &Tree{
				idResolver: resolver,
				assimilateFunc: func(item scanItem) error {
					assimilate = true
					return assimilateErr
				},
				log: &logger,
				options: &options.Options{
					Options: decomposedoptions.Options{
						Root:                      dir,
						MaxConcurrency:            1,
						PersonalSpacePathTemplate: "users/{{.User.Id.OpaqueId}}",
						GeneralSpacePathTemplate:  "shares/{{.SpaceId}}",
						UploadDirectory:           filepath.Join(dir, "uploads"),
					},
				},
			}
			t.Ignorer = ignore.NewIgnorer(t.options, t.log)
			folder = node.New("spaceid", "nodeid", "parentid", "dir", 0, "",
				provider.ResourceType_RESOURCE_TYPE_CONTAINER, nil, fakePathLookup{internalPath: dir})
		})

		When("IDsForPath returns a non-NotFound error", func() {
			It("returns the error and does not attempt assimilation", func() {
				backendErr := errors.New("id cache backend unavailable")
				resolver.EXPECT().IDsForPath(mock.Anything, mock.Anything).Return("", "", backendErr)

				_, err := t.ListFolder(ctx, folder)

				Expect(err).To(MatchError(backendErr))
				Expect(assimilate).To(BeFalse(), "must not assimilate for a non-NotFound error")
			})
		})

		When("an entry is a directory matching ignored_dir_patterns", func() {
			It("is left out without even resolving it", func() {
				t.options.IgnoredDirPatterns = []string{".*"}
				Expect(os.Mkdir(filepath.Join(t.options.Root, ".Trash-1000"), 0700)).To(Succeed())
				// an exact path: the mock fails the spec if the ignored directory is resolved as well
				resolver.EXPECT().IDsForPath(mock.Anything, filepath.Join(t.options.Root, "entry.txt")).
					Return("", "", errtypes.NotFound("path not found in cache"))
				assimilateErr = errors.New("assimilation failed")

				nodes, err := t.ListFolder(ctx, folder)

				Expect(err).ToNot(HaveOccurred())
				Expect(nodes).To(BeEmpty())
			})
		})

		When("IDsForPath returns a NotFound error", func() {
			It("routes the entry to assimilation and does not fail", func() {
				resolver.EXPECT().IDsForPath(mock.Anything, mock.Anything).
					Return("", "", errtypes.NotFound("path not found in cache"))
				// make assimilation fail so the entry is skipped; a NotFound from
				// IDsForPath must still not surface as a ListFolder error.
				assimilateErr = errors.New("assimilation failed")

				nodes, err := t.ListFolder(ctx, folder)

				Expect(err).ToNot(HaveOccurred())
				Expect(nodes).To(BeEmpty())
				Expect(assimilate).To(BeTrue(), "a NotFound error must route to assimilation")
			})
		})
	})
})
