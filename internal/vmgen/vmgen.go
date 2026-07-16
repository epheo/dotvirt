// Package vmgen builds a KubeVirt VirtualMachine manifest from New-VM wizard
// inputs, using the instancetype+preference model (no OpenShift templates).
package vmgen

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

// Spec is the wizard's input: the choices a user makes for a new VM.
type Spec struct {
	Name         string       `json:"name"`
	Namespace    string       `json:"namespace"`
	Instancetype string       `json:"instancetype"` // cluster instancetype name (size)
	Preference   string       `json:"preference"`   // cluster preference name (OS tuning)
	OSImage      OSImageRef   `json:"osImage"`      // boot DataSource
	DiskSize     string       `json:"diskSize"`     // root disk size, e.g. "30Gi"
	StorageClass string       `json:"storageClass"` // root disk class; empty = cluster default
	Running      bool         `json:"running"`      // start immediately?
	CloudInit    *CloudInit   `json:"cloudInit,omitempty"`
	ExtraDisks   []ExtraDisk  `json:"extraDisks,omitempty"`
	Networks     []NetworkRef `json:"networks,omitempty"` // secondary NAD-backed networks (UDN/localnet)
	// PrimaryNetwork attaches the primary (pod-network) NIC: the masquerade
	// interface backed by the namespace's primary UDN, or the cluster default pod
	// network when none exists. nil/true attaches it; false omits it, leaving a VM
	// whose only NICs are the secondary Networks above. Unlike a pod, a VM need not
	// join the primary network.
	PrimaryNetwork *bool             `json:"primaryNetwork,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
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
	Name         string `json:"name"`
	Size         string `json:"size"`                   // e.g. "10Gi"
	StorageClass string `json:"storageClass,omitempty"` // empty = cluster default
}

type NetworkRef struct {
	Name string `json:"name"` // NAD name, expected as <namespace>/<nad> or <nad>
}

// Manifest builds the VirtualMachine YAML for the spec. It returns the repo path
// the manifest should live at and its content.
func Manifest(s Spec) (path string, content []byte, err error) {
	if err := validateSpec(s); err != nil {
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

func validateSpec(s Spec) error {
	// Name and namespace become the repo path (ns/name.yaml) as well as metadata,
	// so they cross the same DNS-1123 gate as every other create path.
	if err := validate.RequireDNS1123("name", s.Name); err != nil {
		return err
	}
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return err
	}
	switch {
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
	dvTemplates := []any{
		dataVolumeTemplate(rootVol, s.OSImage, orDefault(s.DiskSize, "30Gi"), s.StorageClass),
	}
	// Extra disks are persistent, blank DataVolumes — each on its own (optional)
	// storage class, so they outlive VM restarts and can be storage-migrated later.
	for _, d := range s.ExtraDisks {
		dvTemplates = append(dvTemplates,
			blankDataVolumeTemplate(extraDiskVol(s.Name, d.Name), orDefault(d.Size, "10Gi"), d.StorageClass))
	}
	return map[string]any{
		"runStrategy":         runStrategy(s.Running),
		"instancetype":        map[string]any{"name": s.Instancetype},
		"preference":          map[string]any{"name": s.Preference},
		"dataVolumeTemplates": dvTemplates,
		"template":            template(s, rootVol),
	}
}

func dataVolumeTemplate(name string, img OSImageRef, size, class string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"sourceRef": map[string]any{
				"kind":      "DataSource",
				"name":      img.Name,
				"namespace": img.Namespace,
			},
			"storage": storageBlock(size, class),
		},
	}
}

// blankDataVolumeTemplate provisions an empty, formatted-on-first-boot PVC — the
// backing for an extra data disk.
func blankDataVolumeTemplate(name, size, class string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"source":  map[string]any{"blank": map[string]any{}},
			"storage": storageBlock(size, class),
		},
	}
}

// storageBlock is a DataVolume's storage request. An empty class is omitted so
// the provisioner picks the cluster default.
func storageBlock(size, class string) map[string]any {
	storage := map[string]any{
		"resources": map[string]any{
			"requests": map[string]any{"storage": size},
		},
	}
	if class != "" {
		storage["storageClassName"] = class
	}
	return storage
}

// extraDiskVol is the DataVolume/PVC name for an extra disk: the VM name prefix
// keeps it unique within the namespace (mirrors the "<vm>-rootdisk" root).
func extraDiskVol(vm, disk string) string { return vm + "-" + disk }

func template(s Spec, rootVol string) map[string]any {
	disks := []any{namedDisk("rootdisk")}
	volumes := []any{dataVolumeMount("rootdisk", rootVol)}

	// Extra disks reference the blank DataVolume templates added in vmSpec.
	for _, d := range s.ExtraDisks {
		disks = append(disks, namedDisk(d.Name))
		volumes = append(volumes, dataVolumeMount(d.Name, extraDiskVol(s.Name, d.Name)))
	}

	// The primary NIC (pod network + masquerade) is attached unless explicitly
	// declined — a VM, unlike a pod, may live on secondary networks alone.
	var networks, ifaces []any
	if s.PrimaryNetwork == nil || *s.PrimaryNetwork {
		networks = append(networks, podDefaultNetwork())
		ifaces = append(ifaces, masqueradeIface("default"))
	}
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
