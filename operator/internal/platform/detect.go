// Package platform detects the Kubernetes distribution so the operator can render
// distribution-specific resources (Route vs Ingress) — keeping dotvirt
// platform-agnostic with OpenShift as a first-class profile.
package platform

import (
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// Platform is the detected distribution.
type Platform string

const (
	OpenShift  Platform = "openshift"
	Kubernetes Platform = "kubernetes"
)

// Detect reports OpenShift when the cluster serves the OpenShift API groups (the
// presence of config.openshift.io / route.openshift.io is the canonical signal),
// else vanilla Kubernetes. Errors default to Kubernetes (the portable rendering).
func Detect(cfg *rest.Config) (Platform, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return Kubernetes, err
	}
	groups, err := dc.ServerGroups()
	if err != nil {
		return Kubernetes, err
	}
	for _, g := range groups.Groups {
		switch g.Name {
		case "config.openshift.io", "route.openshift.io":
			return OpenShift, nil
		}
	}
	return Kubernetes, nil
}
