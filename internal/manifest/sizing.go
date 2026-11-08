package manifest

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// sizingFields are the domain fields (paths below spec.template.spec.domain)
// KubeVirt's admission weighs against an instancetype (validateCPU,
// validateMemory). An always field collides with any instancetype; a shared
// one only with an instancetype that sets it too, at the same path below its
// spec. label names a removal in the preview; the unlabeled ones are
// ChangesForEdit's CPU and Memory rows.
var sizingFields = []struct {
	path   string
	always bool
	label  string
}{
	{"cpu.sockets", true, ""},
	{"cpu.cores", true, ""},
	{"cpu.threads", true, ""},
	{"memory.guest", true, ""},
	{"resources.requests.cpu", true, "CPU request"},
	{"resources.requests.memory", true, "Memory request"},
	{"resources.limits.cpu", true, "CPU limit"},
	{"resources.limits.memory", true, "Memory limit"},
	{"cpu.model", false, "CPU model"},
	{"cpu.dedicatedCPUPlacement", false, "Dedicated CPU placement"},
	{"cpu.isolateEmulatorThread", false, "Emulator thread isolation"},
	{"cpu.numa", false, "NUMA"},
	{"cpu.realtime", false, "Realtime"},
	{"memory.hugepages", false, "Hugepages"},
	{"memory.maxGuest", false, "Max guest memory"},
}

// Target is the instancetype an edit switches a VM to, and which shared
// fields it sets as the server read it under the caller's token. The switch
// strips those from the VM and keeps the other shared fields, which KubeVirt
// admits beside it. A Target not read for the edit's instancetype (the zero
// value, a failed read, another name or kind) strips every shared field.
type Target struct {
	// Kind is spec.instancetype.kind as the VM's manifest holds it.
	Kind string   `json:"kind,omitempty"`
	Name string   `json:"name"`
	Read bool     `json:"read,omitempty"`
	Sets []string `json:"sets,omitempty"`
}

// Answers reports whether t was read for ref.
func (t Target) Answers(ref Target) bool {
	return t.Read && t.Kind == ref.Kind && t.Name == ref.Name
}

// SwitchTarget is the instancetype edit switches VM (namespace, name) in
// content to, unread. ok is false when the edit keeps the VM's instancetype,
// leaves it without one, or the VM is not in content.
func SwitchTarget(content []byte, defaultNS, namespace, name string, edit VMEdit) (Target, bool) {
	vm, err := findVM(content, defaultNS, namespace, name)
	if err != nil || vm == nil {
		return Target{}, false
	}
	s := planSizing(vm, edit, Target{})
	return s.ref, s.switched && s.ref.Name != ""
}

// SharedSet is which shared fields an instancetype object sets.
func SharedSet(instancetype map[string]any) []string {
	var out []string
	for _, f := range sizingFields {
		if !f.always && lookup(instancetype, append([]string{"spec"}, strings.Split(f.path, ".")...)...) != nil {
			out = append(out, f.path)
		}
	}
	return out
}

// sizing is how an edit leaves the VM sized, decided once from its manifest so
// the splice, its read-back, the scope check and the preview agree.
type sizing struct {
	instancetype bool     // an instancetype sizes the result
	switched     bool     // and the VM did not reference it: set, or renamed
	ref          Target   // the result's instancetype, unread
	strip        []string // sizingFields paths the edit deletes
}

func planSizing(vm *yaml.Node, edit VMEdit, target Target) sizing {
	cur := get(get(vm, "spec"), "instancetype")
	var s sizing
	switch {
	case edit.Sizing != nil && *edit.Sizing == "custom":
		return s
	case edit.Sizing != nil && *edit.Sizing == "instancetype":
		s.instancetype = true
	default:
		s.instancetype = cur != nil || edit.Instancetype != nil && *edit.Instancetype != ""
	}
	if !s.instancetype {
		return s
	}
	s.ref = Target{Kind: nodeValue(get(cur, "kind")), Name: nodeValue(get(cur, "name"))}
	if edit.Instancetype != nil {
		s.ref.Name = *edit.Instancetype
	}
	s.switched = cur == nil || s.ref.Name != nodeValue(get(cur, "name"))
	known := target.Answers(s.ref)
	for _, f := range sizingFields {
		if f.always || s.switched && (!known || slices.Contains(target.Sets, f.path)) {
			s.strip = append(s.strip, f.path)
		}
	}
	return s
}

// at is the value at path (dotted) below mapping m, nil when absent.
func at(m *yaml.Node, path string) *yaml.Node {
	for k := range strings.SplitSeq(path, ".") {
		m = get(m, k)
	}
	return m
}

// stripPaths deletes from mapping m each key paths names (dotted, below m),
// and whole each mapping those deletions would leave empty; m itself stays.
func stripPaths(ed *lineEditor, m *yaml.Node, prefix string, paths []string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i].Value, m.Content[i+1]
		p := prefix + key
		switch {
		case slices.Contains(paths, p) || emptiedBy(val, p+".", paths):
			ed.deleteChild(m, key)
		case slices.ContainsFunc(paths, func(s string) bool { return strings.HasPrefix(s, p+".") }):
			stripPaths(ed, val, p+".", paths)
		}
	}
}

// emptiedBy reports whether deleting paths leaves mapping n, which holds at
// least one key, with none: an empty mapping already there is not ours to drop.
func emptiedBy(n *yaml.Node, prefix string, paths []string) bool {
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		p := prefix + n.Content[i].Value
		if !slices.Contains(paths, p) && !emptiedBy(n.Content[i+1], p+".", paths) {
			return false
		}
	}
	return true
}
