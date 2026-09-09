package changeset

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// RestoreVersion stages one VM's manifest exactly as commit hash held it: the
// object-level undo. Unlike Revert, which turns a whole merged PR around, this
// touches one file and goes through the caller's draft like any edit, so the
// restore is reviewed and proposed before anything reaches the cluster.
func (c *Coordinator) RestoreVersion(id auth.Identity, proj project.ProjectInfo, namespace, name, hash string) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	vm, found, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !found {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s is not in git", model.ErrNotFound, namespace, name)
	}
	restored, err := read.FileAt(hash, vm.SourceFile)
	if errors.Is(err, git.ErrNoFile) {
		return model.DraftView{}, fmt.Errorf("%w: %s did not exist at %s", model.ErrNotFound, vm.SourceFile, shortCommit(hash))
	}
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrNotFound, err)
	}
	current, ok, err := read.LookupOnBranch(c.baseBranch, vm.SourceFile)
	if err != nil {
		return model.DraftView{}, err
	}
	if ok && bytes.Equal(current, restored) {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches %s", model.ErrInvalid, namespace, name, shortCommit(hash))
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:        draft.KindEdit,
		Namespace:   namespace,
		Name:        name,
		SourceFile:  vm.SourceFile,
		Manifest:    string(restored),
		FromVersion: shortCommit(hash),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}
