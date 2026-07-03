package vmtemplate

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"kubevirt.io/virt-template-engine/template"

	tplv1beta1 "kubevirt.io/virt-template-api/core/v1beta1"

	"github.com/epheo/dotvirt/internal/model"
)

// Rendered is a processed template: the VM manifest ready to stage.
type Rendered struct {
	Name      string
	Namespace string
	Manifest  []byte
	Message   string // the template's post-process message, parameter-substituted
}

// Renderer turns a raw VirtualMachineTemplate manifest plus caller parameters
// into a VM manifest for the target namespace. The in-process engine serves
// today; once the CRD ships in the cluster, the process subresource (called as
// the user) can implement this without touching callers.
type Renderer interface {
	Render(raw []byte, params map[string]string, targetNamespace string) (Rendered, error)
}

// EngineRenderer processes templates with the upstream virt-template engine:
// generated parameter values, ${PARAM} substitution across the whole embedded
// VM, required-parameter enforcement, and strict schema validation of the
// resulting VirtualMachine.
type EngineRenderer struct{}

func (EngineRenderer) Render(raw []byte, params map[string]string, targetNamespace string) (Rendered, error) {
	var tpl tplv1beta1.VirtualMachineTemplate
	if err := yaml.Unmarshal(raw, &tpl); err != nil {
		return Rendered{}, fmt.Errorf("%w: template manifest: %v", model.ErrInvalid, err)
	}
	merged, err := template.MergeParameters(tpl.Spec.Parameters, params)
	if err != nil {
		return Rendered{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	tpl.Spec.Parameters = merged

	vm, msg, ferr := template.GetDefaultProcessor().Process(&tpl)
	if ferr != nil {
		return Rendered{}, fmt.Errorf("%w: %v", model.ErrInvalid, ferr)
	}

	// The engine removes any hardcoded namespace: placement is the deployer's
	// choice, made here.
	vm.Namespace = targetNamespace

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(vm)
	if err != nil {
		return Rendered{}, fmt.Errorf("render %s: %v", tpl.Name, err)
	}
	delete(obj, "status")
	pruneCreationTimestamps(obj)
	out, err := yaml.Marshal(obj)
	if err != nil {
		return Rendered{}, fmt.Errorf("render %s: %v", tpl.Name, err)
	}
	return Rendered{Name: vm.Name, Namespace: targetNamespace, Manifest: out, Message: msg}, nil
}

// pruneCreationTimestamps drops the null creationTimestamp fields the round
// trip through typed structs injects (top-level, dataVolumeTemplates, pod
// template metadata), so the staged manifest reads like a hand-written one.
func pruneCreationTimestamps(m map[string]any) {
	delete(m, "creationTimestamp")
	for _, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			pruneCreationTimestamps(vv)
		case []any:
			for _, e := range vv {
				if em, ok := e.(map[string]any); ok {
					pruneCreationTimestamps(em)
				}
			}
		}
	}
}
