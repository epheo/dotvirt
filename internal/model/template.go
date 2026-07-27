package model

// The template library (Content Library).

// Template is one VirtualMachineTemplate manifest in a library repo's
// templates/ directory — a content-library entry (vSphere: a VM template).
// Name is the file's basename: the deployable identity the API routes carry.
type Template struct {
	Name         string              `json:"name"`
	Library      string              `json:"library"` // owning project, or "platform" (the shared library)
	Description  string              `json:"description,omitempty"`
	SourceFile   string              `json:"sourceFile"` // templates/<name>.yaml
	Parameters   []TemplateParameter `json:"parameters,omitempty"`
	Instancetype string              `json:"instancetype,omitempty"` // blueprint summary, best-effort
	Preference   string              `json:"preference,omitempty"`
	YAML         string              `json:"yaml"`
	Error        string              `json:"error,omitempty"` // parse failure — listed, but not deployable
}

// TemplateParameter mirrors template.kubevirt.io/v1beta1 Parameter, so the
// wizard's form is exactly what the native CRD will accept later.
type TemplateParameter struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Generate    string `json:"generate,omitempty"` // "expression" — value generated from From
	From        string `json:"from,omitempty"`     // the generator's input pattern
	Required    bool   `json:"required,omitempty"`
}

// TemplateList is the content-library listing across the caller's libraries.
type TemplateList struct {
	Templates []Template `json:"templates"`
}

// DeployTemplateRequest renders a library template and stages the resulting VM
// into the target namespace's project draft.
type DeployTemplateRequest struct {
	Library    string            `json:"library"`
	Template   string            `json:"template"`
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name,omitempty"` // overrides the NAME parameter; empty → template default (often generated)
	Parameters map[string]string `json:"parameters,omitempty"`
	PowerOn    bool              `json:"powerOn,omitempty"` // boot the VM once it syncs (templates blueprint Halted)
}

// UpdateTemplateRequest replaces a library template's manifest — editing a
// content-library item. The file updates when the library's PR merges.
type UpdateTemplateRequest struct {
	Library string `json:"library"`
	Name    string `json:"name"`
	YAML    string `json:"yaml"`
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
