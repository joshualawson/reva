// Copyright 2026 OpenCloud GmbH <mail@opencloud.eu>
// SPDX-License-Identifier: Apache-2.0

package options_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
)

func TestIgnoredDirPatterns(t *testing.T) {
	o, err := options.New(map[string]any{"root": t.TempDir(), "ignored_dir_patterns": []string{".*", "@Recycle"}})
	require.NoError(t, err)
	require.Equal(t, []string{".*", "@Recycle"}, o.IgnoredDirPatterns)

	o, err = options.New(map[string]any{"root": t.TempDir()})
	require.NoError(t, err)
	require.Empty(t, o.IgnoredDirPatterns, "off by default")

	_, err = options.New(map[string]any{"root": t.TempDir(), "ignored_dir_patterns": []string{"[unclosed"}})
	require.ErrorContains(t, err, "ignored_dir_patterns", "a malformed glob fails at startup, not on every path")
}

func TestFallbackSpaceOptions(t *testing.T) {
	o, err := options.New(map[string]any{
		"root":                               t.TempDir(),
		"personalspacefallbackpath_template": "cloud/{{.User.Username}}",
		"fallback_uid":                       10100,
		"fallback_gid":                       10100,
	})
	require.NoError(t, err)
	require.Equal(t, "cloud/{{.User.Username}}", o.PersonalSpaceFallbackPathTemplate)
	require.Equal(t, 10100, o.FallbackUID)
	require.Equal(t, 10100, o.FallbackGID)
}
