// Package v1alpha1 is the API for the dotvirt installer operator: a Dotvirt
// resource describes one dotvirt install, and the controller provisions it
// (RBAC, Deployment, Route/Ingress, the AppProject tier + platform Argo app) and
// bootstraps the platform git repo. dotvirt's RUNTIME still owns nothing — this is
// the install-time provisioner, the automated form of today's `oc apply`.
// +kubebuilder:object:generate=true
// +groupName=dotvirt.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the group/version for this API (reuses the dotvirt.io domain the
// project label/annotation already use).
var GroupVersion = schema.GroupVersion{Group: "dotvirt.io", Version: "v1alpha1"}

// SchemeBuilder registers the API types; AddToScheme wires them into a scheme.
var (
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
