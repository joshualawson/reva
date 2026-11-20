package tree

import (
	"context"
	"os"
	"syscall"

	"github.com/pkg/errors"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/usermapper"
	"github.com/opencloud-eu/reva/v3/pkg/storage/utils/middleware"
)

// ownerIDs returns the ids to act as for a space whose root is path: its gid, and its uid only with use_space_owner
func ownerIDs(o *options.Options, path string) (uid, gid int, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return -1, -1, errors.Wrapf(err, "could not stat the space root %q", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, errors.New("no unix ownership information for " + path)
	}
	uid = -1
	if o.UseSpaceOwner {
		uid = int(st.Uid)
	}
	return uid, int(st.Gid), nil
}

// withOwnerIDs stores the ids of the space root in ctx for the scope hooks and workers that run later
func withOwnerIDs(ctx context.Context, o *options.Options, path string) (context.Context, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return ctx, errors.Wrapf(err, "could not stat the space root %q", path)
	}
	st := fi.Sys().(*syscall.Stat_t)
	ctx = context.WithValue(ctx, decomposedfs.CtxKeySpaceGID, st.Gid)
	if o.UseSpaceOwner {
		ctx = context.WithValue(ctx, decomposedfs.CtxKeySpaceUID, st.Uid)
	}
	return ctx, nil
}

// scopeFromContext acts as the ids a previous hook stored in ctx; a no-op when none are stored
func scopeFromContext(ctx context.Context, um usermapper.Mapper) (func() error, error) {
	gid, ok := ctx.Value(decomposedfs.CtxKeySpaceGID).(uint32)
	if !ok {
		return func() error { return nil }, nil
	}
	unscope, err := um.ScopeUserByIds(decomposedfs.SpaceUIDFromContext(ctx), int(gid))
	if err != nil {
		return nil, errors.Wrap(err, "failed to scope user")
	}
	if unscope == nil {
		unscope = func() error { return nil }
	}
	return unscope, nil
}

// SpaceScopeHooks wraps every FS call on a space in the identity of that space's root; empty without use_space_groups
func SpaceScopeHooks(lu *lookup.Lookup, um usermapper.Mapper, o *options.Options) []middleware.Hook {
	if !o.UseSpaceGroups {
		return nil
	}
	resolveSpaceHook := func(methodName string, ctx context.Context, spaceID string) (context.Context, middleware.UnHook, error) {
		if spaceID == "" {
			return ctx, nil, nil
		}
		ctx, err := withOwnerIDs(ctx, o, lu.InternalPath(spaceID, spaceID))
		return ctx, nil, err
	}
	scopeHook := func(methodName string, ctx context.Context, spaceID string) (context.Context, middleware.UnHook, error) {
		if _, ok := ctx.Value(decomposedfs.CtxKeySpaceGID).(uint32); !ok {
			return ctx, nil, nil
		}
		unscope, err := scopeFromContext(ctx, um)
		return ctx, unscope, err
	}
	return []middleware.Hook{resolveSpaceHook, scopeHook}
}

// scopeSpace acts as the root of the space until the returned function runs; a no-op without use_space_groups
func (t *Tree) scopeSpace(spaceID string) (func() error, error) {
	if !t.options.UseSpaceGroups {
		return func() error { return nil }, nil
	}
	root := t.lookup.InternalPath(spaceID, spaceID)
	if root == "" {
		return nil, errors.Errorf("could not resolve the root of space %q", spaceID)
	}
	uid, gid, err := ownerIDs(t.options, root)
	if err != nil {
		return nil, err
	}
	unscope, err := t.userMapper.ScopeUserByIds(uid, gid)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to act as the owner of space %q", spaceID)
	}
	if unscope == nil {
		unscope = func() error { return nil }
	}
	return unscope, nil
}
