package decomposedfs_test

import (
	"os"
	"path/filepath"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	v1beta11 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	ruser "github.com/opencloud-eu/reva/v3/pkg/ctx"
	helpers "github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/testhelpers"
)

// a pre-existing home directory becomes the personal space with adopt_existing_space_roots; without it creation is refused
var _ = Describe("Adopting an existing directory as a personal space root", func() {
	var env *helpers.TestEnv

	AfterEach(func() {
		if env != nil {
			env.Cleanup()
		}
	})

	createSpaceFor := func(username string) (*provider.CreateStorageSpaceResponse, error) {
		env.PermissionsClient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(&cs3permissions.CheckPermissionResponse{
			Status: &v1beta11.Status{Code: v1beta11.Code_CODE_OK},
		}, nil)
		env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(&provider.ResourcePermissions{Stat: true, AddGrant: true}, nil)
		owner := &userpb.User{
			Id:       &userpb.UserId{Idp: "idp", OpaqueId: "owner-" + username, Type: userpb.UserType_USER_TYPE_PRIMARY},
			Username: username,
		}
		ctx := ruser.ContextSetUser(env.Ctx, owner)
		return env.Fs.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{Owner: owner, Type: "personal"})
	}

	It("adopts the directory and keeps its contents", func() {
		var err error
		env, err = helpers.NewTestEnv(map[string]any{"adopt_existing_space_roots": true})
		Expect(err).ToNot(HaveOccurred())

		home := filepath.Join(env.Root, "users", "alice")
		Expect(os.MkdirAll(filepath.Join(home, "Photos"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(home, "Photos", "old.jpg"), []byte("x"), 0o644)).To(Succeed())

		res, err := createSpaceFor("alice")
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Status.Code).To(Equal(v1beta11.Code_CODE_OK))

		// the space root is the pre-existing directory, and what was in it is still there
		Expect(env.Lookup.InternalPath(res.StorageSpace.Root.SpaceId, res.StorageSpace.Root.SpaceId)).To(Equal(home))
		Expect(os.ReadFile(filepath.Join(home, "Photos", "old.jpg"))).To(Equal([]byte("x")))
	})

	It("still refuses a directory that already is a space", func() {
		var err error
		env, err = helpers.NewTestEnv(map[string]any{"adopt_existing_space_roots": true})
		Expect(err).ToNot(HaveOccurred())

		_, err = createSpaceFor("bob")
		Expect(err).ToNot(HaveOccurred())
		_, err = createSpaceFor("bob")
		Expect(err).To(MatchError(ContainSubstring("already exists")))
	})

	It("refuses an existing directory without the option", func() {
		var err error
		env, err = helpers.NewTestEnv(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(env.Root, "users", "carol"), 0o755)).To(Succeed())

		_, err = createSpaceFor("carol")
		Expect(err).To(MatchError(ContainSubstring("already exists")))
	})
})
