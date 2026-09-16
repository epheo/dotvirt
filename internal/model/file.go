package model

// TemplatesDir is the library directory within a repo: VirtualMachineTemplate
// manifests the ArgoCD Applications exclude from the applied path, so the git
// plane's inventory walk skips it and the library read covers only it.
const TemplatesDir = "templates"

// File is a repo-relative path with the bytes it holds: what the git plane
// reads and writes, what a renderer produces, and what the cluster exports for
// adoption.
type File struct {
	Path    string
	Content []byte
}

// Adoptable is one live object serialized as the manifest a repo would hold:
// what the cluster captures under the caller's token and the coordinator
// stages, the coordinator never reading the cluster itself.
type Adoptable struct {
	Namespace string
	Name      string
	Kind      string
	Path      string // repo-relative
	Manifest  []byte
}
