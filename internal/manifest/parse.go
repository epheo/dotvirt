package manifest

import (
	"bytes"
	"fmt"
	"io"

	"github.com/epheo/dotvirt/internal/model"
	"gopkg.in/yaml.v3"
)

// vmDoc is the minimal shape of a VirtualMachine manifest dotvirt reads for the
// inventory. Only the fields shown in the vCenter view are decoded.
type vmDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		// KubeVirt supports both; runStrategy is preferred, running is legacy.
		RunStrategy  string `yaml:"runStrategy"`
		Running      *bool  `yaml:"running"`
		Instancetype *struct {
			Name string `yaml:"name"`
		} `yaml:"instancetype"`
		Preference *struct {
			Name string `yaml:"name"`
		} `yaml:"preference"`
		// DataVolumeTemplates carry the provisioned disks' size + storage class
		// (CDI accepts both storage and the legacy pvc spec form).
		DataVolumeTemplates []struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Storage *dvStorageSpec `yaml:"storage"`
				PVC     *dvStorageSpec `yaml:"pvc"`
			} `yaml:"spec"`
		} `yaml:"dataVolumeTemplates"`
		Template struct {
			Metadata struct {
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
			Spec struct {
				EvictionStrategy string `yaml:"evictionStrategy"`
				Domain           struct {
					CPU struct {
						Cores int `yaml:"cores"`
					} `yaml:"cpu"`
					Memory struct {
						Guest string `yaml:"guest"`
					} `yaml:"memory"`
					Resources struct {
						Requests struct {
							Memory string `yaml:"memory"`
						} `yaml:"requests"`
					} `yaml:"resources"`
					Devices struct {
						Disks []struct {
							Name string `yaml:"name"`
						} `yaml:"disks"`
						Interfaces []struct {
							Name string `yaml:"name"`
						} `yaml:"interfaces"`
					} `yaml:"devices"`
				} `yaml:"domain"`
				Networks []struct {
					Name   string         `yaml:"name"`
					Pod    map[string]any `yaml:"pod"`
					Multus *struct {
						NetworkName string `yaml:"networkName"`
					} `yaml:"multus"`
				} `yaml:"networks"`
				Volumes []struct {
					Name       string `yaml:"name"`
					DataVolume *struct {
						Name string `yaml:"name"`
					} `yaml:"dataVolume"`
					PVC *struct {
						ClaimName string `yaml:"claimName"`
					} `yaml:"persistentVolumeClaim"`
					ContainerDisk map[string]any `yaml:"containerDisk"`
					CloudInit     map[string]any `yaml:"cloudInitNoCloud"`
					EmptyDisk     *struct {
						Capacity string `yaml:"capacity"`
					} `yaml:"emptyDisk"`
				} `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// ParseVMs decodes every VirtualMachine doc in a manifest file into model.VM,
// tagging each with the source path. defaultNS is used when a manifest omits
// metadata.namespace.
func ParseVMs(path string, content []byte, defaultNS string) ([]model.VM, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var vms []model.VM
	for {
		var doc vmDoc
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if doc.Kind != "VirtualMachine" {
			continue
		}
		ns := doc.Metadata.Namespace
		if ns == "" {
			ns = defaultNS
		}
		_, drsExclude := doc.Spec.Template.Metadata.Annotations[PreferNoEvictionAnnotation]
		vms = append(vms, model.VM{
			Namespace:        ns,
			Name:             doc.Metadata.Name,
			Power:            powerFromDoc(doc),
			CPUCores:         doc.Spec.Template.Spec.Domain.CPU.Cores,
			Memory:           memoryFromDoc(doc),
			Instancetype:     refName(doc.Spec.Instancetype),
			Preference:       refName(doc.Spec.Preference),
			Labels:           doc.Metadata.Labels,
			DRSExclude:       drsExclude,
			EvictionStrategy: doc.Spec.Template.Spec.EvictionStrategy,
			Disks:            disksFromDoc(doc),
			Networks:         networksFromDoc(doc),
			SourceFile:       path,
			Sync:             model.SyncUnknown,
		})
	}
	return vms, nil
}

func refName(ref *struct {
	Name string `yaml:"name"`
}) string {
	if ref == nil {
		return ""
	}
	return ref.Name
}

// dvStorageSpec is the size + class part of a DataVolume's storage (or legacy
// pvc) spec.
type dvStorageSpec struct {
	StorageClassName string `yaml:"storageClassName"`
	Resources        struct {
		Requests struct {
			Storage string `yaml:"storage"`
		} `yaml:"requests"`
	} `yaml:"resources"`
}

// disksFromDoc derives disk devices, joining each disk with its volume to label
// the type, and dataVolume volumes with their dataVolumeTemplates for the
// provisioned size + storage class.
func disksFromDoc(d vmDoc) []model.Disk {
	// DV template name → (size, class), from whichever spec form is present.
	type dvInfo struct{ size, class string }
	dvs := map[string]dvInfo{}
	for _, t := range d.Spec.DataVolumeTemplates {
		spec := t.Spec.Storage
		if spec == nil {
			spec = t.Spec.PVC
		}
		if spec == nil {
			continue
		}
		dvs[t.Metadata.Name] = dvInfo{size: spec.Resources.Requests.Storage, class: spec.StorageClassName}
	}

	ts := d.Spec.Template.Spec
	volType := map[string]model.Disk{}
	for _, v := range ts.Volumes {
		disk := model.Disk{Name: v.Name}
		switch {
		case v.DataVolume != nil:
			disk.Type = "dataVolume"
			if info, ok := dvs[v.DataVolume.Name]; ok {
				disk.Size, disk.StorageClass = info.size, info.class
			}
		case v.PVC != nil:
			disk.Type = "pvc"
		case v.ContainerDisk != nil:
			disk.Type = "containerDisk"
		case v.CloudInit != nil:
			disk.Type = "cloudInitNoCloud"
		case v.EmptyDisk != nil:
			disk.Type = "emptyDisk"
			disk.Size = v.EmptyDisk.Capacity
		}
		volType[v.Name] = disk
	}
	var out []model.Disk
	for _, dk := range ts.Domain.Devices.Disks {
		if info, ok := volType[dk.Name]; ok {
			out = append(out, info)
		} else {
			out = append(out, model.Disk{Name: dk.Name})
		}
	}
	return out
}

func networksFromDoc(d vmDoc) []model.NIC {
	ts := d.Spec.Template.Spec
	netType := map[string]string{}
	for _, n := range ts.Networks {
		switch {
		case n.Pod != nil:
			netType[n.Name] = "pod"
		case n.Multus != nil:
			netType[n.Name] = n.Multus.NetworkName
		}
	}
	var out []model.NIC
	for _, iface := range ts.Domain.Devices.Interfaces {
		out = append(out, model.NIC{Name: iface.Name, Network: netType[iface.Name]})
	}
	return out
}

func powerFromDoc(d vmDoc) model.Power {
	switch d.Spec.RunStrategy {
	case "Always", "RerunOnFailure":
		return model.PowerOn
	case "Halted":
		return model.PowerOff
	case "Manual", "Once":
		// State is controlled out-of-band; treat as unknown from manifest alone.
		return model.PowerUnknown
	}
	if d.Spec.Running != nil {
		if *d.Spec.Running {
			return model.PowerOn
		}
		return model.PowerOff
	}
	return model.PowerUnknown
}

// memoryFromDoc prefers domain.memory.guest, falling back to the legacy
// resources.requests.memory.
func memoryFromDoc(d vmDoc) string {
	if g := d.Spec.Template.Spec.Domain.Memory.Guest; g != "" {
		return g
	}
	return d.Spec.Template.Spec.Domain.Resources.Requests.Memory
}
