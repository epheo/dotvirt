// Package vmgen builds a KubeVirt VirtualMachine manifest from New-VM wizard
// inputs, using the instancetype+preference model (no OpenShift templates).
package vmgen

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Spec is the wizard's input: the choices a user makes for a new VM.
type Spec struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Instancetype string            `json:"instancetype"` // cluster instancetype name (size)
	Preference   string            `json:"preference"`   // cluster preference name (OS tuning)
	OSImage      OSImageRef        `json:"osImage"`      // boot DataSource
	DiskSize     string            `json:"diskSize"`     // root disk size, e.g. "30Gi"
	StorageClass string            `json:"storageClass"` // root disk class; empty = cluster default
	Running      bool              `json:"running"`      // start immediately?
	CloudInit    *CloudInit        `json:"cloudInit,omitempty"`
	ExtraDisks   []ExtraDisk       `json:"extraDisks,omitempty"`
	Networks     []NetworkRef      `json:"networks,omitempty"` // extra NAD-backed networks (besides pod default)
	Labels       map[string]string `json:"labels,omitempty"`
}

type OSImageRef struct {
	Name      string `json:"name"`      // DataSource name
	Namespace string `json:"namespace"` // DataSource namespace
}

type CloudInit struct {
	User          string `json:"user,omitempty"`
	Password      string `json:"password,omitempty"`
	SSHKey        string `json:"sshKey,omitempty"`
	ExtraUserData string `json:"extraUserData,omitempty"` // raw appended #cloud-config lines
}

type ExtraDisk struct {
	Name string `json:"name"`
	Size string `json:"size"` // e.g. "10Gi"
}

type NetworkRef struct {
	Name string `json:"name"` // NAD name, expected as <namespace>/<nad> or <nad>
}

// Manifest builds the VirtualMachine YAML for the spec. It returns the repo path
// the manifest should live at and its content.
func Manifest(s Spec) (path string, content []byte, err error) {
	if err := validate(s); err != nil {
		return "", nil, err
	}

	vm := map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   metadata(s),
		"spec":       vmSpec(s),
	}

	out, err := yaml.Marshal(vm)
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/" + s.Name + ".yaml", out, nil
}

func validate(s Spec) error {
	switch {
	case s.Name == "":
		return fmt.Errorf("name is required")
	case s.Namespace == "":
		return fmt.Errorf("namespace is required")
	case s.Instancetype == "":
		return fmt.Errorf("instancetype is required")
	case s.Preference == "":
		return fmt.Errorf("preference is required")
	case s.OSImage.Name == "":
		return fmt.Errorf("osImage is required")
	}
	return nil
}

func metadata(s Spec) map[string]any {
	m := map[string]any{"name": s.Name, "namespace": s.Namespace}
	if len(s.Labels) > 0 {
		m["labels"] = toAnyMap(s.Labels)
	}
	return m
}

func vmSpec(s Spec) map[string]any {
	rootVol := s.Name + "-rootdisk"
	spec := map[string]any{
		"runStrategy":  runStrategy(s.Running),
		"instancetype": map[string]any{"name": s.Instancetype},
		"preference":   map[string]any{"name": s.Preference},
		"dataVolumeTemplates": []any{
			dataVolumeTemplate(rootVol, s.OSImage, orDefault(s.DiskSize, "30Gi"), s.StorageClass),
		},
		"template": template(s, rootVol),
	}
	return spec
}

func dataVolumeTemplate(name string, img OSImageRef, size, class string) map[string]any {
	storage := map[string]any{
		"resources": map[string]any{
			"requests": map[string]any{"storage": size},
		},
	}
	// Empty = omit, so the provisioner picks the cluster default class.
	if class != "" {
		storage["storageClassName"] = class
	}
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"sourceRef": map[string]any{
				"kind":      "DataSource",
				"name":      img.Name,
				"namespace": img.Namespace,
			},
			"storage": storage,
		},
	}
}

func template(s Spec, rootVol string) map[string]any {
	disks := []any{namedDisk("rootdisk")}
	volumes := []any{dataVolumeMount("rootdisk", rootVol)}

	// Extra blank disks become their own dataVolumeTemplates referenced here;
	// for simplicity in the manifest we add them as volumes with emptyDisk.
	for _, d := range s.ExtraDisks {
		disks = append(disks, namedDisk(d.Name))
		volumes = append(volumes, map[string]any{
			"name":      d.Name,
			"emptyDisk": map[string]any{"capacity": d.Size},
		})
	}

	networks := []any{podDefaultNetwork()}
	ifaces := []any{masqueradeIface("default")}
	for _, n := range s.Networks {
		net, iface := multusNetwork(n.Name)
		networks = append(networks, net)
		ifaces = append(ifaces, iface)
	}

	if ci := cloudInit(s); ci != nil {
		disks = append(disks, namedDisk("cloudinitdisk"))
		volumes = append(volumes, ci)
	}

	domain := map[string]any{
		"devices": map[string]any{
			"disks":      disks,
			"interfaces": ifaces,
		},
	}

	return map[string]any{
		"spec": map[string]any{
			"domain":   domain,
			"networks": networks,
			"volumes":  volumes,
		},
	}
}

func namedDisk(name string) map[string]any {
	return map[string]any{"name": name, "disk": map[string]any{"bus": "virtio"}}
}

func dataVolumeMount(name, dvName string) map[string]any {
	return map[string]any{"name": name, "dataVolume": map[string]any{"name": dvName}}
}

func podDefaultNetwork() map[string]any {
	return map[string]any{"name": "default", "pod": map[string]any{}}
}

func masqueradeIface(name string) map[string]any {
	return map[string]any{"name": name, "masquerade": map[string]any{}}
}

// multusNetwork builds a NAD-backed (multus) network + bridge interface. ref is
// "<namespace>/<nad>" or "<nad>"; the interface name is derived from the NAD name.
func multusNetwork(ref string) (network map[string]any, iface map[string]any) {
	ifaceName := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ifaceName = ref[i+1:]
	}
	network = map[string]any{
		"name":   ifaceName,
		"multus": map[string]any{"networkName": ref},
	}
	iface = map[string]any{"name": ifaceName, "bridge": map[string]any{}}
	return network, iface
}

// cloudInit builds a cloudInitNoCloud volume from the spec, or nil if no
// cloud-init was requested.
func cloudInit(s Spec) map[string]any {
	if s.CloudInit == nil {
		return nil
	}
	ci := s.CloudInit
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	if ci.User != "" {
		b.WriteString("user: " + ci.User + "\n")
	}
	if ci.Password != "" {
		b.WriteString("password: " + ci.Password + "\n")
		b.WriteString("chpasswd: { expire: False }\n")
		b.WriteString("ssh_pwauth: True\n")
	}
	if ci.SSHKey != "" {
		b.WriteString("ssh_authorized_keys:\n  - " + ci.SSHKey + "\n")
	}
	if ci.ExtraUserData != "" {
		b.WriteString(ci.ExtraUserData)
		if !strings.HasSuffix(ci.ExtraUserData, "\n") {
			b.WriteString("\n")
		}
	}

	return map[string]any{
		"name":             "cloudinitdisk",
		"cloudInitNoCloud": map[string]any{"userData": b.String()},
	}
}

func runStrategy(running bool) string {
	if running {
		return "Always"
	}
	return "Halted"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}
