// Copyright 2026 OpenCloud GmbH <mail@opencloud.eu>
// SPDX-License-Identifier: Apache-2.0

package posix_test

import (
	"os"
	"path/filepath"
	"testing"

	providerv1beta1 "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/errtypes"
	posixhelpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
)

var fallbackConfig = map[string]any{
	"adopt_existing_space_roots":         true,
	"personalspacefallbackpath_template": "cloud/{{.User.Username}}",
}

func withRoot(root string) map[string]any {
	config := map[string]any{"root": root}
	for k, v := range fallbackConfig {
		config[k] = v
	}
	return config
}

// a user without a directory at the primary template gets the space under the fallback template, and finds it again
func TestFallbackSpace_CreatedUnderFallbackTemplate(t *testing.T) {
	root := t.TempDir()
	env, err := posixhelpers.NewTestEnv(withRoot(root))
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)

	spaceID := env.SpaceRootRes.SpaceId
	require.Equal(t, filepath.Join(root, "cloud", "username"), env.Lookup.InternalPath(spaceID, spaceID))
	_, err = os.Stat(filepath.Join(root, "users", "username"))
	require.True(t, os.IsNotExist(err), "the primary location only adopts, it is never created")

	id, err := env.Lookup.GenerateSpaceID("personal", env.Owner)
	require.NoError(t, err)
	require.Equal(t, spaceID, id, "the fallback root is where the user's space id lives now")
	_, err = env.Fs.CreateStorageSpace(env.Ctx, &providerv1beta1.CreateStorageSpaceRequest{Owner: env.Owner, Type: "personal", Name: "username"})
	require.ErrorAs(t, err, new(errtypes.AlreadyExists), "never a second space for the same user")
}

// a directory at the primary template still wins: it is adopted and nothing appears under the fallback
func TestFallbackSpace_PrimaryDirectoryIsAdopted(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "users", "username")
	require.NoError(t, os.MkdirAll(home, 0770))
	env, err := posixhelpers.NewTestEnv(withRoot(root))
	require.NoError(t, err)
	t.Cleanup(env.Cleanup)

	spaceID := env.SpaceRootRes.SpaceId
	require.Equal(t, home, env.Lookup.InternalPath(spaceID, spaceID))
	_, err = os.Stat(filepath.Join(root, "cloud"))
	require.True(t, os.IsNotExist(err))
}
