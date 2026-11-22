package ignore_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/blobstore"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/ignore"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	decomposedoptions "github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/tree/propagator"
	"github.com/rs/zerolog"
)

var _ = Describe("Ignore", func() {
	var (
		logger  = zerolog.Nop()
		ignorer = ignore.NewIgnorer(&options.Options{
			Options: decomposedoptions.Options{
				Root:                      "/storage",
				PersonalSpacePathTemplate: "users/{{.User.Id.OpaqueId}}",
				GeneralSpacePathTemplate:  "spaces/{{.Space.Id.OpaqueId}}",
				UploadDirectory:           "/storage/uploads",
			},
		}, &logger)
	)
	DescribeTable("IsIgnored",
		func(path string, want bool) {
			Expect(ignorer.IsIgnored(path)).To(Equal(want))
		},
		Entry("matches index paths", filepath.Join("/storage", lookup.IndexesDir, "something"), true),
		Entry("matches changes paths", filepath.Join("/storage", propagator.ChangesDir, "node1"), true),
		Entry("matches upload directory", filepath.Join("/storage", "uploads", "upload.bin"), true),
		Entry("matches metadata dir", filepath.Join("/storage", "users", "user1", lookup.MetadataDir), true),
		Entry("matches metadata dir child", filepath.Join("/storage", "users", "user1", lookup.MetadataDir, "a"), true),
		Entry("matches metadata dir deep child", filepath.Join("/storage", "users", "user1", lookup.MetadataDir, "a", "b"), true),
		Entry("matches temporary dir", filepath.Join("/storage", "users", "user1", blobstore.TMPDir), true),
		Entry("matches temporary child", filepath.Join("/storage", "users", "user1", blobstore.TMPDir, "a"), true),
		Entry("matches purged personal space child", filepath.Join("/storage", "users", lookup.PurgeDirPrefix+"user1-x", "a"), true),
		Entry("matches purged project space child", filepath.Join("/storage", "spaces", lookup.PurgeDirPrefix+"space1-x", "a", "b"), true),
		Entry("does not match purge prefix deeper in a space", filepath.Join("/storage", "users", "user1", lookup.PurgeDirPrefix+"a"), false),
		Entry("matches temporary deep child", filepath.Join("/storage", "users", "user1", blobstore.TMPDir, "a", "b"), true),
		Entry("matches trash paths", filepath.Join("/storage", "users", "user1", lookup.TrashDir), true),
		Entry("matches trash child", filepath.Join("/storage", "users", "user1", lookup.TrashDir, "a"), true),
		Entry("matches trash deep child", filepath.Join("/storage", "users", "user1", lookup.TrashDir, "a", "b"), true),

		Entry("does not match internal paths in other locations", filepath.Join("/storage", "users", "user1", "a", lookup.MetadataDir), false),
		Entry("does not match regular user content in space root", filepath.Join("/storage", "users", "user1", "a.txt"), false),
		Entry("does not match regular user content", filepath.Join("/storage", "users", "user1", "documents", "a.txt"), false),
		Entry("does not match temporary dir in other locations", filepath.Join("/storage", "users", "user1", "a", blobstore.TMPDir), false),
		Entry("does not match trash dir in other locations", filepath.Join("/storage", "users", "user1", "a", lookup.TrashDir), false),
		Entry("does not match trash dir in other locations", filepath.Join("/storage", "users", "user1", "a", "foo.trashitem"), false),
		Entry("does not match trash dir in other locations", filepath.Join("/storage", "users", "user1", "a", "foo.trashinfo"), false),

		Entry("does not match metadata substring matches", filepath.Join("/storage", "users", "user1", "a"+lookup.MetadataDir), false),
		Entry("does not match temporary substring matches", filepath.Join("/storage", "users", "user1", "a"+blobstore.TMPDir), false),
		Entry("does not match trash substring matches", filepath.Join("/storage", "users", "user1", "a"+lookup.TrashDir), false),
		Entry("does not match metadata substring matches", filepath.Join("/storage", "users", "user1", lookup.MetadataDir+"a"), false),
		Entry("does not match temporary substring matches", filepath.Join("/storage", "users", "user1", blobstore.TMPDir+"a"), false),
		Entry("does not match trash substring matches", filepath.Join("/storage", "users", "user1", lookup.TrashDir+"a"), false),
	)

	Describe("IsIgnoredDir", func() {
		var (
			root    string
			ignorer *ignore.Ignorer
		)
		BeforeEach(func() {
			root = GinkgoT().TempDir()
			ignorer = ignore.NewIgnorer(&options.Options{
				Options: decomposedoptions.Options{
					Root:                              root,
					PersonalSpacePathTemplate:         "users/{{.User.Username}}",
					PersonalSpaceFallbackPathTemplate: "cloud/{{.User.Username}}",
					GeneralSpacePathTemplate:          "spaces/{{.Space.Id.OpaqueId}}",
					UploadDirectory:                   filepath.Join(root, "uploads"),
				},
				IgnoredDirPatterns: []string{".*", "@Recycle"},
			}, &logger)
			for _, dir := range []string{"users/user1/.Trash-1000/files", "users/user1/docs/.git/objects", "users/user1/docs", "users/@Recycle", "cloud/troy"} {
				Expect(os.MkdirAll(filepath.Join(root, dir), 0700)).To(Succeed())
			}
			Expect(os.WriteFile(filepath.Join(root, "users/user1/docs/.hidden-file"), []byte("x"), 0600)).To(Succeed())
		})

		It("matches a directory at the top of a space", func() {
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/.Trash-1000"))).To(BeTrue())
			Expect(ignorer.IsIgnored(filepath.Join(root, "users/user1/.Trash-1000/files/a.txt"))).To(BeTrue())
		})

		It("matches a directory at any depth", func() {
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs/.git"))).To(BeTrue())
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs/.git/objects/ab"))).To(BeTrue())
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/@Recycle/anything"))).To(BeTrue())
		})

		It("leaves files alone, even hidden ones", func() {
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs/.hidden-file"))).To(BeFalse())
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs/.not-there-yet"))).To(BeFalse())
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs"))).To(BeFalse())
		})

		It("only matches whole names below the root", func() {
			Expect(ignorer.IsIgnoredDir(filepath.Join(root, "users/user1/docs/my@Recycle"))).To(BeFalse())
			Expect(ignorer.IsIgnoredDir("/elsewhere/.git")).To(BeFalse())
		})

		It("is off without patterns", func() {
			plain := ignore.NewIgnorer(&options.Options{Options: decomposedoptions.Options{Root: root}}, &logger)
			Expect(plain.IsIgnoredDir(filepath.Join(root, "users/user1/.Trash-1000"))).To(BeFalse())
		})

		It("treats the fallback personal spaces root like the others", func() {
			Expect(ignorer.IsRootPath(filepath.Join(root, "cloud"))).To(BeTrue())
			Expect(ignorer.IsSpaceRoot(filepath.Join(root, "cloud/troy"))).To(BeTrue())
			Expect(ignorer.IsMetadata(filepath.Join(root, "cloud/troy", lookup.MetadataDir, "a"))).To(BeTrue())
			Expect(ignorer.IsPurging(filepath.Join(root, "cloud", lookup.PurgeDirPrefix+"troy-x", "a"))).To(BeTrue())
		})
	})
})
