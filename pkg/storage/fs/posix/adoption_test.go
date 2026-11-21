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
	"github.com/pkg/xattr"
	"github.com/stretchr/testify/require"

	posixhelpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
)

// a NAS home exists before OpenCloud does: owned by its user, carrying Samba's attributes, no space metadata
func TestAdoptExistingHome(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("adoption with use_space_owner needs root (setfsuid)")
	}
	root := t.TempDir()
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
	root := t.TempDir()
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
