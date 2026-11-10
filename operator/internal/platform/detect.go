// Package platform detects the Kubernetes distribution so the operator can render
// distribution-specific resources (Route vs Ingress) - keeping dotvirt
// platform-agnostic with OpenShift as a first-class profile.
package platform

import (
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// else vanilla Kubernetes. On error the value is a placeholder: main.go fails
// startup rather than render for a guessed platform.
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

// ServesClusterVersion reports whether the cluster serves
// config.openshift.io/v1 clusterversions: a full OpenShift, which its cluster
// version operator runs. Detect reads MicroShift as OpenShift (it serves
// route.openshift.io, and some config.openshift.io kinds), but MicroShift has
// no cluster version.
func ServesClusterVersion(cfg *rest.Config) (bool, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}
	list, err := dc.ServerResourcesForGroupVersion("config.openshift.io/v1")
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(list.APIResources, func(r metav1.APIResource) bool { return r.Name == "clusterversions" }), nil
}
