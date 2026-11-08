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
// restore is reviewed and proposed before anything reaches the cluster, and
// passes the gate as any whole-file edit does.
func (c *Coordinator) RestoreVersion(id auth.Identity, proj project.ProjectInfo, resource, namespace, name, hash string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.onBase(read, hash); err != nil {
		return model.DraftView{}, err
	}
	path, err := c.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	restored, err := read.FileAt(hash, path)
	if errors.Is(err, git.ErrNoFile) {
		return model.DraftView{}, fmt.Errorf("%w: %s did not exist at %s", model.ErrNotFound, path, shortCommit(hash))
	}
	if err != nil {
		return model.DraftView{}, gitFailed(hash, err)
	}
	current, ok, err := read.LookupOnBranch(c.baseBranch, path)
	if err != nil {
		return model.DraftView{}, err
	}
	if ok && bytes.Equal(current, restored) {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches %s", model.ErrInvalid, namespace, name, shortCommit(hash))
	}
	return c.stage(id, proj, draft.Entry{
		Kind:        draft.KindEdit,
		Resource:    draft.Resource(resource),
		Namespace:   namespace,
		Name:        name,
		SourceFile:  path,
		Manifest:    string(restored),
		FromVersion: shortCommit(hash),
		BaseBlob:    pinOf(current, ok),
	})
}

// onBase refuses a commit the base branch never held: restore and revert
// bring back past states of base, never content only another branch or an
// open proposal carries.
func (r *Reader) onBase(read *git.Repo, hash string) error {
	ok, err := read.OnBranch(r.baseBranch, hash)
	if err != nil {
		return gitFailed(hash, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s is not in the history of %s", model.ErrNotFound, shortCommit(hash), r.baseBranch)
	}
	return nil
}
