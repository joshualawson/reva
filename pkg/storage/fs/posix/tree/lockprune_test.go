// Copyright 2026 OpenCloud GmbH <mail@opencloud.eu>
// SPDX-License-Identifier: Apache-2.0

package tree_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/metadata"
)

var _ = Describe("Lock pruning", func() {
	It("removes only the stale locks of gone nodes and of assimilations", func() {
		subtree, err := generateRandomString(10)
		Expect(err).ToNot(HaveOccurred())
		dir := filepath.Join(env.Root, "users", env.Owner.Username, subtree)
		Expect(os.Mkdir(dir, 0700)).To(Succeed())
		Eventually(func(g Gomega) {
			n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes, Path: "/" + subtree})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(n.Exists).To(BeTrue())
		}).Should(Succeed())
		n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes, Path: "/" + subtree})
		Expect(err).ToNot(HaveOccurred())

		locks := filepath.Join(env.Root, "users", env.Owner.Username, lookup.MetadataDir, metadata.LocksDir)
		Expect(os.MkdirAll(locks, 0700)).To(Succeed())
		live := filepath.Join(locks, n.ID+".mlock")
		orphan := filepath.Join(locks, uuid.New().String()+".mlock")
		temp := env.Tree.TempLockfilePathForTest(n.SpaceID, dir)
		young := env.Tree.TempLockfilePathForTest(n.SpaceID, dir+"-young")
		for _, f := range []string{live, orphan, temp, young} {
			Expect(os.WriteFile(f, nil, 0600)).To(Succeed())
		}
		old := time.Now().Add(-48 * time.Hour)
		for _, f := range []string{live, orphan, temp} {
			Expect(os.Chtimes(f, old, old)).To(Succeed())
		}

		env.Tree.PruneStaleLocks(env.Ctx, 24*time.Hour)

		Expect(live).To(BeAnExistingFile())
		Expect(young).To(BeAnExistingFile())
		Expect(orphan).ToNot(BeAnExistingFile())
		Expect(temp).ToNot(BeAnExistingFile())
	})
})
