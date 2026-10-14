package install

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// routerPeers admit the OpenShift router whatever its endpoint publishing: the
// ingress policy group covers router pods, the host-network group a HostNetwork
// router, and the namespace name covers MicroShift, whose router namespace
// carries neither group label.
func routerPeers() []networkingv1.NetworkPolicyPeer {
	return []networkingv1.NetworkPolicyPeer{
		nsPeer(map[string]string{"policy-group.network.openshift.io/ingress": ""}),
		nsPeer(map[string]string{"policy-group.network.openshift.io/host-network": ""}),
		nsPeer(map[string]string{corev1.LabelMetadataName: "openshift-ingress"}),
	}
}

func nsPeer(match map[string]string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: match}}
}

// networkPolicy admits ingress to the selected pods' HTTP port only from the
// install namespace, the operator namespace (the forge bootstrap calls the
// Service), the ArgoCD namespace (plugin generator, repo-server on an
// in-cluster forge URL) and the router. operatorNS "" omits that peer (an
// out-of-cluster `make run`).
func networkPolicy(dv *dotvirtv1alpha1.Dotvirt, name string, selector map[string]string, port int32, argoNS, operatorNS string) *networkingv1.NetworkPolicy {
	from := []networkingv1.NetworkPolicyPeer{
		{PodSelector: &metav1.LabelSelector{}},
		nsPeer(map[string]string{corev1.LabelMetadataName: argoNS}),
	}
	if operatorNS != "" && operatorNS != dv.Namespace && operatorNS != argoNS {
		from = append(from, nsPeer(map[string]string{corev1.LabelMetadataName: operatorNS}))
	}
	from = append(from, routerPeers()...)
	tcp := corev1.ProtocolTCP
	p := intstr.FromInt32(port)
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: objectMeta(name, dv),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: selector},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &p}},
				From:  from,
			}},
		},
	}
}

// OperandNetworkPolicy fences the dotvirt pod. OpenShift only: on vanilla
// Kubernetes the ingress controller's namespace is unknowable, and a wrong guess
// cuts the console off.
func OperandNetworkPolicy(dv *dotvirtv1alpha1.Dotvirt, argoNS, operatorNS string) *networkingv1.NetworkPolicy {
	return networkPolicy(dv, AppName, selectorLabels, HTTPPort, argoNS, operatorNS)
}

// ForgejoNetworkPolicy fences the managed Forgejo pod; OpenShift only, as above.
func ForgejoNetworkPolicy(dv *dotvirtv1alpha1.Dotvirt, argoNS, operatorNS string) *networkingv1.NetworkPolicy {
	return networkPolicy(dv, ForgejoServiceName, forgejoSelector, ForgejoHTTPPort, argoNS, operatorNS)
}
