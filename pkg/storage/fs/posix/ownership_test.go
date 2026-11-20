//go:build ownership

// Root-only: it switches the fsuid to the owner of the space, which needs CAP_SETUID.
// Run with: go test -tags ownership -run TestOwnership ./pkg/storage/fs/posix/

package posix_test

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/storage"
	posixblobstore "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/blobstore"
	posixhelpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
)

// the uid and gid the NAS would show for the space owner; nothing else on the box uses them
const (
	ownerUID = 10001
	ownerGID = 10001
)

// with use_space_owner every entry written through the FS belongs to the owner of the space root
func TestOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("acting as the space owner needs root (setfsuid)")
	}
	env, root := exercise(t, map[string]any{"use_space_owner": true})
	assertOwnedBy(t, root, ownerUID, ownerGID)
	assertNoRootOwned(t, env.Options.UploadDirectory)
}

// the control: the same flow without the option leaves root-owned entries, so the assertions above mean something
func TestOwnership_WithoutOptionFilesBelongToTheProcess(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root for the comparison")
	}
	_, root := exercise(t, nil)
	require.NotEmpty(t, rootOwnedEntries(t, root), "without use_space_owner the process should own what it writes")
}

// exercise runs every write path through the scoped FS on a space whose root is owned by ownerUID
func exercise(t *testing.T, config map[string]any) (*posixhelpers.TestEnv, string) {
	t.Helper()
	env, err := posixhelpers.NewTestEnv(config)
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)

	env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything).Return(&provider.ResourcePermissions{
		Stat: true, GetPath: true, GetQuota: true, ListContainer: true, CreateContainer: true,
		InitiateFileUpload: true, InitiateFileDownload: true, Delete: true, Move: true,
		ListRecycle: true, RestoreRecycleItem: true, PurgeRecycle: true, ListFileVersions: true,
	}, nil)
	env.Permissions.On("AssembleTrashPermissions", mock.Anything, mock.Anything).Return(&provider.ResourcePermissions{
		Stat: true, ListRecycle: true, RestoreRecycleItem: true, PurgeRecycle: true, Delete: true, Move: true,
	}, nil)

	// the env mocks the blobstore; write through the real one so bytes land on disk
	bs, err := posixblobstore.New(env.Root)
	require.NoError(t, err)
	env.Blobstore.On("Upload", mock.Anything, mock.Anything, mock.Anything).Return(bs.Upload).Maybe()
	env.Blobstore.On("UploadFromReader", mock.Anything, mock.Anything, mock.Anything).Return(bs.UploadFromReader).Maybe()
	env.Blobstore.On("Download", mock.Anything).Return(bs.Download).Maybe()
	env.Blobstore.On("Delete", mock.Anything).Return(bs.Delete).Maybe()

	// the env created the space as root; on the NAS the home directory belongs to its user
	spaceID := env.SpaceRootRes.SpaceId
	root := env.Lookup.InternalPath(spaceID, spaceID)
	require.NotEmpty(t, root)
	chownTree(t, root, ownerUID, ownerGID)
	require.NoError(t, env.Tree.Setup(), "root and upload dir permissions for space owners")

	fsys := env.ScopedFS
	ctx := env.Ctx
	ref := func(p string) *provider.Reference {
		return &provider.Reference{ResourceId: env.SpaceRootRes, Path: p}
	}
	upload := func(path string, content []byte, existed bool) {
		if !existed {
			_, err := fsys.TouchFile(ctx, ref(path), false, "")
			require.NoError(t, err, "touch %s", path)
		}
		session := "session-" + filepath.Base(path) + "-" + strconv.Itoa(len(content))
		_, err = fsys.PrepareUpload(ctx, ref(path), session, storage.UploadInfo{NodeExisted: true, Size: int64(len(content))})
		require.NoError(t, err, "prepare %s", path)
		require.NoError(t, fsys.CommitUpload(ctx, ref(path), session, storage.UploadSource{
			Body:   io.NopCloser(bytes.NewReader(content)),
			Length: int64(len(content)),
		}), "commit %s", path)
		require.NoError(t, fsys.MarkProcessing(ctx, ref(path), false, session), "unmark %s", path)
	}

	// directories
	_, err = fsys.CreateDir(ctx, ref("./photos"))
	require.NoError(t, err)
	_, err = fsys.CreateDir(ctx, ref("./photos/2026"))
	require.NoError(t, err)

	// a new file, then a second upload to the same file so a revision is written
	upload("./photos/2026/a.jpg", bytes.Repeat([]byte("a"), 2048), false)
	upload("./photos/2026/a.jpg", bytes.Repeat([]byte("b"), 4096), true)

	// an empty file, moved
	_, err = fsys.TouchFile(ctx, ref("./notes.txt"), false, "")
	require.NoError(t, err)
	_, err = fsys.Move(ctx, ref("./notes.txt"), ref("./photos/notes.txt"))
	require.NoError(t, err)

	// into the trash and back
	_, err = fsys.Delete(ctx, ref("./photos/2026"))
	require.NoError(t, err)
	items, err := fsys.ListRecycle(ctx, ref("."), "", "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = fsys.RestoreRecycleItem(ctx, ref("."), items[0].Key, "", nil)
	require.NoError(t, err)

	// into the trash for good
	_, err = fsys.Delete(ctx, ref("./photos/notes.txt"))
	require.NoError(t, err)
	items, err = fsys.ListRecycle(ctx, ref("."), "", "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, fsys.PurgeRecycleItem(ctx, ref("."), items[0].Key, ""))

	// the restored file is readable with its last content
	_, rc, err := fsys.Download(ctx, ref("./photos/2026/a.jpg"), func(*provider.ResourceInfo) bool { return true })
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Len(t, got, 4096)

	return env, root
}

func chownTree(t *testing.T, root string, uid, gid int) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	}))
}

func assertOwnedBy(t *testing.T, root string, uid, gid int) {
	t.Helper()
	var wrong []string
	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		sys := st.Sys().(*syscall.Stat_t)
		if int(sys.Uid) != uid || int(sys.Gid) != gid {
			rel, _ := filepath.Rel(root, path)
			wrong = append(wrong, rel+" uid="+strconv.Itoa(int(sys.Uid))+" gid="+strconv.Itoa(int(sys.Gid)))
		}
		return nil
	}))
	require.Empty(t, wrong, "entries not owned by %d:%d", uid, gid)
}

func assertNoRootOwned(t *testing.T, dir string) {
	t.Helper()
	require.Empty(t, rootOwnedEntries(t, dir), "root-owned leftovers")
}

func rootOwnedEntries(t *testing.T, dir string) []string {
	t.Helper()
	var owned []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.Sys().(*syscall.Stat_t).Uid == 0 {
			owned = append(owned, path)
		}
		return nil
	}))
	return owned
}
