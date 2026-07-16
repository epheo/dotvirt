package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Condition types the controller sets on a Dotvirt's status. Available is the
// roll-up other tooling watches; the rest explain a not-yet-ready install.
const (
	// ConditionDependenciesReady is True when the cluster has the operators dotvirt
	// needs (ArgoCD; KubeVirt; and — for the networking tier — OVN-K + NMState).
	ConditionDependenciesReady = "DependenciesReady"
	// ConditionForgeReady is True when a managed Forgejo is up and bootstrapped (its
	// admin + scoped token + owner org); irrelevant for a BYO forge.
	ConditionForgeReady = "ForgeReady"
	// ConditionWorkloadReady is True when the namespaced workload (ServiceAccount,
	// PVC, Service, Deployment, exposure) is applied.
	ConditionWorkloadReady = "WorkloadReady"
	// ConditionArgoReady is True when the cluster-scoped RBAC bindings and the
	// AppProject tier (plus the platform Application) are applied.
	ConditionArgoReady = "ArgoReady"
	// ConditionForgeRepoReady is True when the platform git repo exists (the
	// install-time imperative bootstrap a pure-declarative installer can't do).
	ConditionForgeRepoReady = "ForgeRepoReady"
	// ConditionArgoWebhook is True when the forge→ArgoCD instant-sync webhook is
	// registered (org-level); Unknown when no Argo URL is resolvable (poll fallback).
	ConditionArgoWebhook = "ArgoWebhook"
	// ConditionAvailable is the roll-up: the full install is reconciled and serving.
	ConditionAvailable = "Available"
)

// Phase values the controller writes to Status.Phase. Plain string consts (the
// field stays a string) so the API surface is unchanged.
const (
	PhaseReady                 = "Ready"
	PhaseProvisioning          = "Provisioning"
	PhaseBlockedOnDependencies = "BlockedOnDependencies"
)

// IngressType selects how the dotvirt Route is exposed. "auto" picks Route on
// OpenShift and Ingress on vanilla Kubernetes (the operator detects the platform).
// +kubebuilder:validation:Enum=auto;route;ingress;gateway
type IngressType string

// ForgeSpec points dotvirt at its git forge and the platform-tier repo. The forge
// credential here is the INSTALL-TIME admin token the operator uses to create the
// platform repo — distinct from (and more privileged than) dotvirt's runtime
// clone/push token, preserving the install-provisioner vs runtime-owns-nothing split.
type ForgeSpec struct {
	// URL is the forge base (e.g. https://forgejo.example.com).
	URL string `json:"url,omitempty"`
	// PlatformRepo is the cluster-scoped + tenancy repo (CUDN/NNCP/Namespace). The
	// operator ensures it exists; dotvirt routes platform creates here by kind.
	PlatformRepo string `json:"platformRepo,omitempty"`
	// Managed deploys a self-hosted Forgejo for evaluation; false = bring your own.
	Managed bool `json:"managed,omitempty"`
	// CredentialsSecret names a Secret holding the forge-admin credential used for
	// the platform-repo bootstrap (keys: url, username, token).
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// InsecureTLS skips TLS verification when calling the forge API (a self-signed forge
	// Route, e.g. the bundled Forgejo). DEV/EVAL ONLY — never enable against a forge with
	// a trusted certificate.
	InsecureTLS bool `json:"insecureTLS,omitempty"`
}

// ArgoCDSpec locates the ArgoCD install dotvirt rides. Defaults suit OpenShift
// GitOps (openshift-gitops); override for community ArgoCD (argocd /
// argocd-application-controller). The operator binds the apply RBAC + AppProjects
// to this controller ServiceAccount.
type ArgoCDSpec struct {
	Namespace                string `json:"namespace,omitempty"`
	ControllerServiceAccount string `json:"controllerServiceAccount,omitempty"`
	// ServerURL is the externally reachable ArgoCD base URL the forge posts webhooks
	// to (…/api/webhook) for instant sync. Empty = discover the OpenShift GitOps
	// server Route; if neither resolves, the webhook is skipped (Argo falls back to
	// its poll).
	ServerURL string `json:"serverURL,omitempty"`
}

// IngressSpec controls how the UI is exposed.
type IngressSpec struct {
	Type IngressType `json:"type,omitempty"`
	Host string      `json:"host,omitempty"`
}

// MetricsSpec points the Performance tab at a Prometheus/Thanos query API; empty
// disables it.
type MetricsSpec struct {
	URL string `json:"url,omitempty"`
}

// DotvirtSpec is the desired dotvirt install.
type DotvirtSpec struct {
	// Image is the dotvirt app image to deploy.
	Image   string      `json:"image,omitempty"`
	Forge   ForgeSpec   `json:"forge,omitempty"`
	ArgoCD  ArgoCDSpec  `json:"argocd,omitempty"`
	Ingress IngressSpec `json:"ingress,omitempty"`
	Metrics MetricsSpec `json:"metrics,omitempty"`
}

// DotvirtStatus is the observed install state.
type DotvirtStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is a short human-facing summary (e.g. Pending, Provisioning, Ready).
	Phase string `json:"phase,omitempty"`
	// Conditions follow the standard k8s conventions (see the Condition* consts).
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Dotvirt is one dotvirt install. Namespaced singleton in the operator's namespace;
// the operator itself holds the cluster RBAC to provision cluster-scoped resources.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=dv
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Dotvirt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DotvirtSpec   `json:"spec,omitempty"`
	Status DotvirtStatus `json:"status,omitempty"`
}

// DotvirtList is a list of Dotvirt.
// +kubebuilder:object:root=true
type DotvirtList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Dotvirt `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Dotvirt{}, &DotvirtList{})
}
