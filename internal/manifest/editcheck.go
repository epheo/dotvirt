package manifest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// validateEdit refuses an edit carrying a value the API server or KubeVirt
// would reject, before anything is spliced, naming every such value at once.
// Removals only name what the manifest already holds and are never written,
// so they are not checked: a hand-written name outside these rules must stay
// removable.
func validateEdit(e VMEdit) error {
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	if e.Power != nil {
		add(validate.RequireOneOf("power", *e.Power, string(model.PowerOn), string(model.PowerOff)))
	}
	if e.CPUCores != nil && *e.CPUCores < 1 {
		add(fmt.Errorf("%w: CPU count %d must be at least 1", model.ErrInvalid, *e.CPUCores))
	}
	if e.Memory != nil {
		add(validate.RequireQuantity("memory", *e.Memory))
	}
	if e.Instancetype != nil {
		add(validate.RequireObjectName("instance type", *e.Instancetype))
	}
	if e.Preference != nil {
		add(validate.RequireObjectName("preference", *e.Preference))
	}
	if e.Sizing != nil {
		add(validate.RequireOneOf("sizing", *e.Sizing, "custom", "instancetype"))
	}
	add(validate.RequireLabels("label", e.SetLabels))
	if e.EvictionStrategy != nil {
		add(validate.RequireOneOf("eviction strategy", *e.EvictionStrategy, "", "None", "LiveMigrate", "LiveMigrateIfPossible", "External"))
	}
	for _, d := range e.AddDisks {
		add(validate.RequireDNS1123("disk name", d.Name))
		if d.Size != "" {
			add(validate.RequireQuantity("disk size", d.Size))
		}
		if d.StorageClass != "" {
			add(validate.RequireObjectName("storage class", d.StorageClass))
		}
	}
	for _, n := range e.AddNetworks {
		// A network is a NetworkAttachmentDefinition, bare or <namespace>/<name>;
		// its name also becomes the interface name.
		ns, name, qualified := strings.Cut(n.Name, "/")
		if qualified {
			add(validate.RequireDNS1123("network namespace", ns))
		} else {
			name = ns
		}
		add(validate.RequireDNS1123("network", name))
	}
	for _, m := range e.MigrateVolumes {
		add(validate.RequireObjectName("storage class", m.StorageClass))
	}
	if e.Pin != nil {
		for _, h := range *e.Pin {
			add(validate.RequireObjectName("host", h))
		}
	}
	for _, g := range e.AddGroups {
		add(validate.RequireDNS1123("group name", g.Name))
		add(validate.RequireOneOf("group mode", g.Mode, "together", "apart"))
	}
	return errors.Join(errs...)
}
