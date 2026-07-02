package install

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// routeGVK renders Routes as unstructured so the operator needn't import the
// OpenShift API.
var routeGVK = schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}

// Route exposes the named Service (its "http" port, edge TLS at the router) on
// OpenShift, as a Route of the same name. host may be empty — the router then
// assigns one. In the dotvirt namespace, so it's owner-referenced like the other
// namespaced resources.
func Route(dv *dotvirtv1alpha1.Dotvirt, name, host string) *unstructured.Unstructured {
	spec := map[string]any{
		"to":   map[string]any{"kind": "Service", "name": name},
		"port": map[string]any{"targetPort": "http"},
		"tls":  map[string]any{"termination": "edge", "insecureEdgeTerminationPolicy": "Redirect"},
	}
	if host != "" {
		spec["host"] = host
	}
	return unstructuredObject(routeGVK, name, dv.Namespace, dv.Name, spec)
}

// Ingress exposes the named Service on vanilla Kubernetes, as an Ingress of the
// same name. TLS is left to the cluster's ingress controller / cert-manager (no
// cert secret is assumed here).
func Ingress(dv *dotvirtv1alpha1.Dotvirt, name string, port int32, host string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: objectMeta(name, dv.Namespace, dv.Name),
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: name,
									Port: networkingv1.ServiceBackendPort{Number: port},
								},
							},
						}},
					},
				},
			}},
		},
	}
}
