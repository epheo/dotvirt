package manifest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// GroupLabelPrefix marks a placement-group membership label on the VM
// template. The label propagates template -> VMI -> virt-launcher pod, which
// is what the pod (anti-)affinity terms match, so membership and rule ride
// the same key.
const GroupLabelPrefix = "group.scheduling.dotvirt.io/"

const hostnameLabel = "kubernetes.io/hostname"

// affinityDoc is the decoded shape of spec.template.spec.affinity — only what
// is needed to recognize dotvirt's own terms; anything beyond them flags the
// VM as hand-tuned (Custom) and the scheduling editor refuses to touch it.
type affinityDoc struct {
	NodeAffinity *struct {
		Required *struct {
			Terms []nodeTermDoc `yaml:"nodeSelectorTerms"`
		} `yaml:"requiredDuringSchedulingIgnoredDuringExecution"`
		Preferred []map[string]any `yaml:"preferredDuringSchedulingIgnoredDuringExecution"`
	} `yaml:"nodeAffinity"`
	PodAffinity     *podAffinityDoc `yaml:"podAffinity"`
	PodAntiAffinity *podAffinityDoc `yaml:"podAntiAffinity"`
}

type nodeTermDoc struct {
	MatchExpressions []struct {
		Key      string   `yaml:"key"`
		Operator string   `yaml:"operator"`
		Values   []string `yaml:"values"`
	} `yaml:"matchExpressions"`
	MatchFields []map[string]any `yaml:"matchFields"`
}

type podAffinityDoc struct {
	Required  []affinityTermDoc `yaml:"requiredDuringSchedulingIgnoredDuringExecution"`
	Preferred []struct {
		Weight int             `yaml:"weight"`
		Term   affinityTermDoc `yaml:"podAffinityTerm"`
	} `yaml:"preferredDuringSchedulingIgnoredDuringExecution"`
}

type affinityTermDoc struct {
	LabelSelector struct {
		MatchLabels      map[string]string `yaml:"matchLabels"`
		MatchExpressions []map[string]any  `yaml:"matchExpressions"`
	} `yaml:"labelSelector"`
	TopologyKey       string         `yaml:"topologyKey"`
	Namespaces        []string       `yaml:"namespaces"`
	NamespaceSelector map[string]any `yaml:"namespaceSelector"`
}

// schedulingFromParts derives a VM's placement policy from its template
// labels, nodeSelector and affinity. pinViaSelector reports that the pin came
// from the hostname nodeSelector rather than a node-affinity term — the
// editor must not duplicate such a pin into a regenerated affinity block.
// Returns nil when the VM carries no placement content at all.
func schedulingFromParts(labels, nodeSelector map[string]string, aff *affinityDoc) (s *model.VMScheduling, pinViaSelector bool) {
	out := model.VMScheduling{}
	groups := map[string]model.PlacementGroup{}
	for k, v := range labels {
		if name, ok := strings.CutPrefix(k, GroupLabelPrefix); ok {
			groups[name] = model.PlacementGroup{Name: name, Mode: v}
		}
	}
	if h := nodeSelector[hostnameLabel]; h != "" {
		out.Pin = []string{h}
		pinViaSelector = true
	}

	if aff != nil {
		if na := aff.NodeAffinity; na != nil {
			if len(na.Preferred) > 0 {
				out.Custom = true
			}
			if na.Required != nil {
				if pin, ok := pinFromTerms(na.Required.Terms); ok {
					if pinViaSelector {
						// Two competing host constraints — not a shape the editor writes.
						out.Custom = true
					}
					out.Pin = pin
					pinViaSelector = false
				} else {
					out.Custom = true
				}
			}
		}
		collect := func(section *podAffinityDoc, mode string) {
			if section == nil {
				return
			}
			for _, t := range section.Required {
				if name, ok := groupTerm(t, mode); ok {
					groups[name] = model.PlacementGroup{Name: name, Mode: mode, Strict: true}
				} else {
					out.Custom = true
				}
			}
			for _, p := range section.Preferred {
				if name, ok := groupTerm(p.Term, mode); ok {
					groups[name] = model.PlacementGroup{Name: name, Mode: mode}
				} else {
					out.Custom = true
				}
			}
		}
		collect(aff.PodAffinity, "together")
		collect(aff.PodAntiAffinity, "apart")
	}

	if len(groups) == 0 && len(out.Pin) == 0 && !out.Custom {
		return nil, false
	}
	out.Groups = sortedGroups(groups)
	return &out, pinViaSelector
}

// pinFromTerms recognizes the one node-affinity shape the editor writes: a
// single term whose only expression is hostname In [hosts...].
func pinFromTerms(terms []nodeTermDoc) ([]string, bool) {
	if len(terms) != 1 || len(terms[0].MatchFields) != 0 || len(terms[0].MatchExpressions) != 1 {
		return nil, false
	}
	e := terms[0].MatchExpressions[0]
	if e.Key != hostnameLabel || e.Operator != "In" || len(e.Values) == 0 {
		return nil, false
	}
	return e.Values, true
}

// groupTerm recognizes a dotvirt-written pod (anti-)affinity term: hostname
// topology, a single matchLabel under GroupLabelPrefix whose value is the
// section's mode, and nothing else.
func groupTerm(t affinityTermDoc, mode string) (string, bool) {
	if t.TopologyKey != hostnameLabel ||
		len(t.LabelSelector.MatchExpressions) != 0 ||
		len(t.Namespaces) != 0 || t.NamespaceSelector != nil ||
		len(t.LabelSelector.MatchLabels) != 1 {
		return "", false
	}
	for k, v := range t.LabelSelector.MatchLabels {
		if name, ok := strings.CutPrefix(k, GroupLabelPrefix); ok && v == mode {
			return name, true
		}
	}
	return "", false
}

// currentScheduling reads the placement policy off the (unedited) node tree.
// Line edits are queued, never applied to the tree, so this is safe to call
// at any point during ApplyEdit.
func currentScheduling(vmRoot *yaml.Node) (*model.VMScheduling, bool) {
	tmpl := get(get(vmRoot, "spec"), "template")
	labels := decodeStringMap(get(get(tmpl, "metadata"), "labels"))
	ts := get(tmpl, "spec")
	nodeSel := decodeStringMap(get(ts, "nodeSelector"))
	var aff *affinityDoc
	if n := get(ts, "affinity"); n != nil {
		var a affinityDoc
		if err := n.Decode(&a); err != nil {
			return &model.VMScheduling{Custom: true}, false
		}
		aff = &a
	}
	return schedulingFromParts(labels, nodeSel, aff)
}

func decodeStringMap(n *yaml.Node) map[string]string {
	if n == nil {
		return nil
	}
	var m map[string]string
	if err := n.Decode(&m); err != nil {
		return nil
	}
	return m
}

var groupNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// applySchedulingRules rewrites the VM's placement policy: it queues the
// affinity-block replacement and returns the template-label edits for the
// caller to fold into the one applyTemplateMeta pass. Must run BEFORE
// applyTemplateMeta: when both create blocks anchored on the same line, the
// affinity lines (inside spec) have to land before a new metadata: sibling.
func applySchedulingRules(ed *lineEditor, vmRoot *yaml.Node, edit VMEdit) (map[string]string, []string, error) {
	if edit.Pin == nil && len(edit.AddGroups) == 0 && len(edit.RemoveGroups) == 0 {
		return nil, nil, nil
	}
	tmplSpec := templateSpecNode(vmRoot)
	if tmplSpec == nil {
		return nil, nil, fmt.Errorf("%w: VM has no template spec", model.ErrInvalid)
	}
	cur, pinViaSelector := currentScheduling(vmRoot)
	if cur != nil && cur.Custom {
		return nil, nil, fmt.Errorf("%w: the VM carries hand-written affinity or node selection; edit it in git directly", model.ErrConflict)
	}

	groups := map[string]model.PlacementGroup{}
	var pin []string
	if cur != nil {
		for _, g := range cur.Groups {
			groups[g.Name] = g
		}
		pin = cur.Pin
	}

	set := map[string]string{}
	var remove []string
	for _, g := range edit.AddGroups {
		if !groupNameRe.MatchString(g.Name) || len(g.Name) > 63 {
			return nil, nil, fmt.Errorf("%w: group name %q must be lowercase alphanumeric with dashes (max 63 chars)", model.ErrInvalid, g.Name)
		}
		if g.Mode != "together" && g.Mode != "apart" {
			return nil, nil, fmt.Errorf("%w: group mode %q must be together or apart", model.ErrInvalid, g.Mode)
		}
		groups[g.Name] = g
		set[GroupLabelPrefix+g.Name] = g.Mode
	}
	for _, n := range edit.RemoveGroups {
		delete(groups, n)
		remove = append(remove, GroupLabelPrefix+n)
	}

	if edit.Pin != nil {
		pin = nil
		for _, h := range *edit.Pin {
			h = strings.TrimSpace(h)
			if h == "" {
				return nil, nil, fmt.Errorf("%w: empty host in pin list", model.ErrInvalid)
			}
			pin = append(pin, h)
		}
		// A hostname nodeSelector (the legacy single-host pin) would double-
		// constrain placement beside the regenerated node-affinity term.
		applyMapEdits(ed, tmplSpec, "nodeSelector", nil, []string{hostnameLabel})
	} else if pinViaSelector {
		// The pin lives in nodeSelector and is not being edited: leave it
		// there rather than duplicating it into the affinity block.
		pin = nil
	}

	ed.deleteChild(tmplSpec, "affinity")
	if block := affinityBlock(pin, sortedGroups(groups)); len(block) > 0 {
		ed.insertBlock(tmplSpec, block)
	}
	return set, remove, nil
}

// affinityBlock renders the full affinity section for the desired policy —
// the editor always rewrites it wholesale (it refuses hand-written content),
// so rendering stays deterministic: pin, then together, then apart, groups
// sorted by name.
func affinityBlock(pin []string, groups []model.PlacementGroup) []string {
	var together, apart []model.PlacementGroup
	for _, g := range groups {
		if g.Mode == "together" {
			together = append(together, g)
		} else {
			apart = append(apart, g)
		}
	}
	if len(pin) == 0 && len(together) == 0 && len(apart) == 0 {
		return nil
	}
	b := []string{"affinity:"}
	if len(pin) > 0 {
		b = append(b,
			"  nodeAffinity:",
			"    requiredDuringSchedulingIgnoredDuringExecution:",
			"      nodeSelectorTerms:",
			"        - matchExpressions:",
			"            - key: "+hostnameLabel,
			"              operator: In",
			"              values:",
		)
		for _, h := range pin {
			b = append(b, "                - "+h)
		}
	}
	b = append(b, podAffinitySection("podAffinity", together)...)
	b = append(b, podAffinitySection("podAntiAffinity", apart)...)
	return b
}

func podAffinitySection(field string, groups []model.PlacementGroup) []string {
	var req, pref []model.PlacementGroup
	for _, g := range groups {
		if g.Strict {
			req = append(req, g)
		} else {
			pref = append(pref, g)
		}
	}
	if len(req) == 0 && len(pref) == 0 {
		return nil
	}
	b := []string{"  " + field + ":"}
	if len(req) > 0 {
		b = append(b, "    requiredDuringSchedulingIgnoredDuringExecution:")
		for _, g := range req {
			b = append(b,
				"      - labelSelector:",
				"          matchLabels:",
				"            "+quoteKey(GroupLabelPrefix+g.Name)+": "+g.Mode,
				"        topologyKey: "+hostnameLabel,
			)
		}
	}
	if len(pref) > 0 {
		b = append(b, "    preferredDuringSchedulingIgnoredDuringExecution:")
		for _, g := range pref {
			b = append(b,
				"      - weight: 100",
				"        podAffinityTerm:",
				"          labelSelector:",
				"            matchLabels:",
				"              "+quoteKey(GroupLabelPrefix+g.Name)+": "+g.Mode,
				"          topologyKey: "+hostnameLabel,
			)
		}
	}
	return b
}

func sortedGroups(m map[string]model.PlacementGroup) []model.PlacementGroup {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]model.PlacementGroup, 0, len(m))
	for _, n := range names {
		out = append(out, m[n])
	}
	return out
}
