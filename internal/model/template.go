package model

// The template library (Content Library).

// Template is one VirtualMachineTemplate manifest in a library repo's
// templates/ directory - a content-library entry.
// Name is the file's basename: the deployable identity the API routes carry.
type Template struct {
	Name         string              `json:"name"`
	Library      string              `json:"library"` // owning project, or "platform" (the shared library)
	Description  string              `json:"description,omitempty"`
	SourceFile   string              `json:"sourceFile"` // templates/<name>.yaml
	Parameters   []TemplateParameter `json:"parameters,omitempty"`
	Instancetype string              `json:"instancetype,omitempty"` // blueprint summary, best-effort
	Preference   string              `json:"preference,omitempty"`
	// Image is the boot image ("namespace/name" DataSource) the blueprint's
	// first disk is made from, parameters resolved by their defaults; empty
	// when a parameter without a default names it.
	Image    string `json:"image,omitempty"`
	YAML     string `json:"yaml"`
	BaseBlob string `json:"baseBlob,omitempty"` // git blob ID of YAML, for UpdateTemplateRequest
	Error    string `json:"error,omitempty"`    // parse failure - listed, but not deployable
}

// TemplateParameter mirrors template.kubevirt.io/v1beta1 Parameter, so the
// wizard's form is exactly what the native CRD will accept later.
type TemplateParameter struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Generate    string `json:"generate,omitempty"` // "expression" - value generated from From
	From        string `json:"from,omitempty"`     // the generator's input pattern
	Required    bool   `json:"required,omitempty"`
}

// TemplateList is the content-library listing across the caller's libraries.
// Seeds reports the starter library's pull request, or why it could not be
// opened, per library that has one pending.
type TemplateList struct {
	Templates []Template     `json:"templates"`
	Seeds     []TemplateSeed `json:"seeds,omitempty"`
}

// TemplateSeed is the starter template library proposed to a project's repo:
// its open pull request, or the forge's refusal when it could not be opened.
type TemplateSeed struct {
	Library  string `json:"library"`
	PRNumber int    `json:"prNumber,omitempty"`
	PRURL    string `json:"prURL,omitempty"`
	Error    string `json:"error,omitempty"`
}

// DeployTemplateRequest renders a library template and stages the resulting VM
// into the target namespace's project draft.
type DeployTemplateRequest struct {
	Library    string            `json:"library"`
	Template   string            `json:"template"`
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name,omitempty"` // overrides the NAME parameter; empty -> template default (often generated)
	Parameters map[string]string `json:"parameters,omitempty"`
	PowerOn    bool              `json:"powerOn,omitempty"` // boot the VM once it syncs (templates blueprint Halted)
}

// UpdateTemplateRequest replaces a library template's manifest - editing a
// content-library item. The file updates when the library's PR merges.
type UpdateTemplateRequest struct {
	Library string `json:"library"`
	Name    string `json:"name"`
	YAML    string `json:"yaml"`
	// BaseBlob is the version the edit was made on (Template.BaseBlob); a
	// stale one is refused. Optional.
	BaseBlob string `json:"baseBlob,omitempty"`
}

// SaveTemplateRequest derives a template from an existing VM's git manifest and
// stages it into the chosen library ("Clone to Template").
type SaveTemplateRequest struct {
	Library         string `json:"library"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	SourceNamespace string `json:"sourceNamespace"`
	SourceName      string `json:"sourceName"`
}
