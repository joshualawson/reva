package tree_test

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	helpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs"
)

func TestUseSpaceOwnerImpliesSpaceGroups(t *testing.T) {
	o, err := options.New(map[string]any{"root": t.TempDir(), "use_space_owner": true})
	require.NoError(t, err)
	require.True(t, o.UseSpaceOwner)
	require.True(t, o.UseSpaceGroups)

	o, err = options.New(map[string]any{"root": t.TempDir(), "use_space_groups": true})
	require.NoError(t, err)
	require.False(t, o.UseSpaceOwner)
}

// ScopeToSpace stores the uid of the space root only with use_space_owner; the NullMapper makes it runnable as any user
func TestScopeToSpaceStoresTheOwnerIDs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the env uses the real mapper; covered by the ownership tests")
	}
	env, err := helpers.NewTestEnv(map[string]any{"use_space_owner": true})
	require.NoError(t, err)
	defer env.Cleanup()

	spaceID := env.SpaceRootRes.SpaceId
	fi, err := os.Stat(env.Lookup.InternalPath(spaceID, spaceID))
	require.NoError(t, err)
	st := fi.Sys().(*syscall.Stat_t)

	ctx, unscope, err := env.Tree.ScopeToSpace(env.Ctx, spaceID)
	require.NoError(t, err)
	require.NoError(t, unscope())
	require.Equal(t, st.Gid, ctx.Value(decomposedfs.CtxKeySpaceGID))
	require.Equal(t, st.Uid, ctx.Value(decomposedfs.CtxKeySpaceUID))
	require.Equal(t, int(st.Uid), decomposedfs.SpaceUIDFromContext(ctx))

	// without the option the uid stays unset and the helper says "keep the current one"
	plain, err := helpers.NewTestEnv(map[string]any{"use_space_groups": true})
	require.NoError(t, err)
	defer plain.Cleanup()
	ctx, unscope, err = plain.Tree.ScopeToSpace(plain.Ctx, plain.SpaceRootRes.SpaceId)
	require.NoError(t, err)
	require.NoError(t, unscope())
	require.Nil(t, ctx.Value(decomposedfs.CtxKeySpaceUID))
	require.Equal(t, -1, decomposedfs.SpaceUIDFromContext(ctx))
}
