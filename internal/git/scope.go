package git

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/epheo/dotvirt/internal/model"
)

// inRoot is p inside the folder root, "" being the whole repo. A path that
// would leave the folder is refused: the rest of the repo is someone else's.
func inRoot(root, p string) (string, error) {
	if root == "" {
		return p, nil
	}
	full := path.Join(root, p)
	if !strings.HasPrefix(full, root+"/") {
		return "", fmt.Errorf("%w: %s is outside the project's folder", model.ErrInvalid, p)
	}
	return full, nil
}

// scopeTree is tree's subtree at root; nil, the empty tree to a diff, when
// tree or the folder is absent.
func scopeTree(tree *object.Tree, root string) (*object.Tree, error) {
	if root == "" || tree == nil {
		return tree, nil
	}
	sub, err := tree.Tree(root)
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, nil
	}
	return sub, err
}

// treeAt is branch's head tree, seen from the folder this mirror is scoped to.
// A folder the branch does not hold yet reads as empty.
func (r *Repo) treeAt(branch string) (*object.Tree, error) {
	tree, err := treeFor(r.repo.Load(), branch)
	if err != nil {
		return nil, err
	}
	sub, err := scopeTree(tree, r.root)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return &object.Tree{}, nil
	}
	return sub, nil
}

// scopedDiff is treeDiff seen from the folder this mirror is scoped to.
func (r *Repo) scopedDiff(from, to *object.Tree) ([]FileChange, error) {
	from, err := scopeTree(from, r.root)
	if err != nil {
		return nil, err
	}
	to, err = scopeTree(to, r.root)
	if err != nil {
		return nil, err
	}
	return treeDiff(from, to)
}
