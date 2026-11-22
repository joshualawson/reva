//go:build ownership

// Root-only like the ownership tests: adoption runs with use_space_owner, which switches the fsuid.
// Run with: go test -tags ownership -run TestAdopt ./pkg/storage/fs/posix/
package posix_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	providerv1beta1 "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/pkg/xattr"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/errtypes"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	posixhelpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/metadata"
)

// a NAS home exists before OpenCloud does: owned by its user, carrying Samba's attributes, no space metadata
func TestAdoptExistingHome(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("adoption with use_space_owner needs root (setfsuid)")
	}
	root := traversableTempDir(t)
	home := filepath.Join(root, "users", "username")
	require.NoError(t, os.MkdirAll(home, 0770))
	require.NoError(t, os.WriteFile(filepath.Join(home, "existing.txt"), []byte("kept"), 0660))
	require.NoError(t, xattr.Set(home, "user.DOSATTRIB", []byte("0x10")))
	chownTree(t, home, ownerUID, ownerGID)

	env, err := posixhelpers.NewTestEnv(map[string]any{
		"root":                       root,
		"use_space_owner":            true,
		"adopt_existing_space_roots": true,
	})
	require.NoError(t, err, "creating the personal space should adopt the existing home")
	t.Cleanup(env.Cleanup)

	spaceID := env.SpaceRootRes.SpaceId
	require.Equal(t, home, env.Lookup.InternalPath(spaceID, spaceID), "the home itself is the space root")
	id, err := xattr.Get(home, "user.oc.id")
	require.NoError(t, err)
	require.Equal(t, spaceID, string(id))
	fi, err := os.Stat(home)
	require.NoError(t, err)
	require.EqualValues(t, ownerUID, fi.Sys().(*syscall.Stat_t).Uid, "adoption leaves the home with its owner")
	_, err = os.Stat(filepath.Join(home, "existing.txt"))
	require.NoError(t, err, "what was in the home stays")
	// the storage-wide indexes are written in the base scope, never as the space owner
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "indexes"), func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		fi, err := os.Lstat(p)
		require.NoError(t, err)
		require.EqualValues(t, 0, fi.Sys().(*syscall.Stat_t).Uid, "%s must belong to the service user", p)
		return nil
	}))
}

// the scanner records a home it cannot place in a space with empty ids; that must not become the space id
func TestAdoptExistingHome_IgnoresEmptyCachedID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("adoption with use_space_owner needs root (setfsuid)")
	}
	root := traversableTempDir(t)
	home := filepath.Join(root, "users", "other")
	require.NoError(t, os.MkdirAll(home, 0770))
	env, err := posixhelpers.NewTestEnv(map[string]any{"root": root, "use_space_owner": true, "adopt_existing_space_roots": true})
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)
	require.NoError(t, env.Lookup.IDCache.Set(env.Ctx, "", "", home))

	other := *env.Owner
	other.Username = "other"
	other.Id = &userpb.UserId{Idp: "idp", OpaqueId: "d1c1f7a0-0d3c-4a84-9d5f-1a2b3c4d5e6f", Type: userpb.UserType_USER_TYPE_PRIMARY}
	id, err := env.Lookup.GenerateSpaceID("personal", &other)
	require.NoError(t, err)
	require.NotEmpty(t, id, "an empty cached id is not a space id")
}

// metadata on the root but no index entries is what a crash mid-creation leaves; creating again must repair it
func TestAdoptExistingHome_RelinksAnIndexlessRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("adoption with use_space_owner needs root (setfsuid)")
	}
	root := traversableTempDir(t)
	home := filepath.Join(root, "users", "username")
	require.NoError(t, os.MkdirAll(home, 0770))
	chownTree(t, home, ownerUID, ownerGID)
	env, err := posixhelpers.NewTestEnv(map[string]any{"root": root, "use_space_owner": true, "adopt_existing_space_roots": true})
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)
	spaceID := env.SpaceRootRes.SpaceId

	stale, err := filepath.Glob(filepath.Join(root, "indexes", "*", "*.mpk"))
	require.NoError(t, err)
	require.NotEmpty(t, stale)
	for _, f := range stale {
		require.NoError(t, os.Remove(f))
	}
	_, err = env.Fs.CreateStorageSpace(env.Ctx, &providerv1beta1.CreateStorageSpaceRequest{Owner: env.Owner, Type: "personal", Name: "username"})
	require.ErrorAs(t, err, new(errtypes.AlreadyExists), "the space exists already")
	entries, err := filepath.Glob(filepath.Join(root, "indexes", "by-type", "personal.mpk"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the type index is back")
	byUser, err := filepath.Glob(filepath.Join(root, "indexes", "by-user-id", "*.mpk"))
	require.NoError(t, err)
	require.Len(t, byUser, 1, "the owner index is back")
	_ = spaceID
}

const (
	fallbackUID = 10100
	fallbackGID = 10100
)

// a cloud-only user has no home: the space goes under the fallback template, owned by the fallback identity
func TestFallbackSpace_OwnedByFallbackIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("acting as the space owner needs root (setfsuid)")
	}
	root := traversableTempDir(t)
	env, err := posixhelpers.NewTestEnv(map[string]any{
		"root":                               root,
		"use_space_owner":                    true,
		"adopt_existing_space_roots":         true,
		"personalspacefallbackpath_template": "cloud/{{.User.Username}}",
		"fallback_uid":                       fallbackUID,
		"fallback_gid":                       fallbackGID,
	})
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)

	spaceID := env.SpaceRootRes.SpaceId
	spaceRoot := filepath.Join(root, "cloud", "username")
	require.Equal(t, spaceRoot, env.Lookup.InternalPath(spaceID, spaceID))
	assertOwner(t, spaceRoot, fallbackUID, fallbackGID)
	// what the creation wrote inside ran as that identity, so later owner-scoped calls can lock and offload
	assertOwner(t, filepath.Join(spaceRoot, lookup.MetadataDir), fallbackUID, fallbackGID)
	_, err = os.Stat(filepath.Join(root, "users", "username"))
	require.True(t, os.IsNotExist(err), "nothing is created at the primary template")
}

// internal directories the service wrote as root before it acted as owners deny the owner; startup hands them over
func TestEnsureSpaceHousekeeping_HandsRootOwnedDirsToTheOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("acting as the space owner needs root (setfsuid)")
	}
	root := traversableTempDir(t)
	home := filepath.Join(root, "users", "username")
	require.NoError(t, os.MkdirAll(home, 0770))
	chownTree(t, home, ownerUID, ownerGID)
	env, err := posixhelpers.NewTestEnv(map[string]any{"root": root, "use_space_owner": true, "adopt_existing_space_roots": true})
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)
	require.NoError(t, env.Tree.Setup())
	env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything).Return(&providerv1beta1.ResourcePermissions{
		Stat: true, GetPath: true, ListContainer: true, CreateContainer: true,
	}, nil)
	ref := func(p string) *providerv1beta1.Reference {
		return &providerv1beta1.Reference{ResourceId: env.SpaceRootRes, Path: p}
	}

	metadataDir := filepath.Join(home, lookup.MetadataDir)
	locksDir := filepath.Join(metadataDir, metadata.LocksDir)
	stale := filepath.Join(metadataDir, "stale.mpk")
	require.NoError(t, os.MkdirAll(locksDir, 0700))
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0600))
	for _, p := range []string{metadataDir, locksDir, stale} {
		require.NoError(t, os.Lchown(p, 0, 0))
	}
	_, err = env.ScopedFS.CreateDir(env.Ctx, ref("./before"))
	require.Error(t, err, "the control: with root-owned lock directories the owner cannot write")

	require.NoError(t, env.Tree.EnsureSpaceHousekeeping(home))

	for _, p := range []string{metadataDir, locksDir, stale} {
		assertOwner(t, p, ownerUID, ownerGID)
	}
	_, err = env.ScopedFS.CreateDir(env.Ctx, ref("./after"))
	require.NoError(t, err)
}

func assertOwner(t *testing.T, path string, uid, gid int) {
	t.Helper()
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	st := fi.Sys().(*syscall.Stat_t)
	require.EqualValues(t, uid, st.Uid, "uid of %s", path)
	require.EqualValues(t, gid, st.Gid, "gid of %s", path)
}

// traversableTempDir is t.TempDir() with a parent the space owners can pass through: owner-scoped writes must reach the space
func traversableTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(filepath.Dir(root), 0711))
	return root
}
