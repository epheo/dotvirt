package model

// File is a repo-relative path with the bytes it holds: what the git plane
// reads and writes, what a renderer produces, and what the cluster exports for
// adoption.
type File struct {
	Path    string
	Content []byte
}
