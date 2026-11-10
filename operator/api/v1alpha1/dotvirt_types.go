package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Condition types the controller sets on a Dotvirt's status. Available is the
// roll-up other tooling watches; the rest explain a not-yet-ready install.
const (
	// ConditionDependenciesReady is True when the cluster has the operators dotvirt
	// needs (ArgoCD; KubeVirt; and, for the networking tier, OVN-K + NMState).
	ConditionDependenciesReady = "DependenciesReady"
	// ConditionForgeReady is True when a managed Forgejo is up and bootstrapped (its
	// admin + scoped token + owner org); irrelevant for a BYO forge.
	ConditionForgeReady = "ForgeReady"
	// ConditionSecretsReady is True when the generated secrets (session key, plugin
	// token, webhook secrets, OAuth client secret) exist.
	ConditionSecretsReady = "SecretsReady"
	// ConditionWorkloadReady is True when the namespaced workload (ServiceAccount,
	// PVC, Service, Deployment, exposure) is applied.
	ConditionWorkloadReady = "WorkloadReady"
	// ConditionArgoReady is True when the cluster-scoped RBAC bindings and the
	// AppProject tier (plus the platform Application) are applied.
	ConditionArgoReady = "ArgoReady"
	// ConditionForgeRepoReady is True when the platform git repo exists (the
	// install-time imperative bootstrap a pure-declarative installer can't do).
	ConditionForgeRepoReady = "ForgeRepoReady"
	// ConditionArgoWebhook is True when the forge-to-ArgoCD instant-sync webhook is
	// registered (org-level); Unknown when no Argo URL is resolvable (poll fallback).
	ConditionArgoWebhook = "ArgoWebhook"
	// ConditionDotvirtWebhook is True when the forge-to-dotvirt instant-feedback webhook is
	// registered (org-level); Unknown when no delivery URL is resolvable (git-poll fallback).
	ConditionDotvirtWebhook = "DotvirtWebhook"
	// ConditionSyncImpersonationReady is True once argocd-cm turns on sync
	// impersonation (spec.argocd.syncImpersonation), so ArgoCD syncs each dotvirt
	// Application as its tier's own ServiceAccount; Unknown while not asked;
	// False says what holds it back, that it waits for argocd-cm (Pending), or
	// that the controller's own rights let a tenant DataVolume clone from any
	// namespace (CloneAnywhere). It never gates Available.
	ConditionSyncImpersonationReady = "SyncImpersonationReady"
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

// IngressType selects how the dotvirt UI is exposed. "auto" picks a Gateway when
// spec.ingress.gateway names one, else Route on OpenShift and Ingress on vanilla
// Kubernetes (the operator detects the platform).
// +kubebuilder:validation:Enum=auto;route;ingress;gateway
type IngressType string

const (
	IngressAuto    IngressType = "auto"
	IngressRoute   IngressType = "route"
	IngressIngress IngressType = "ingress"
	IngressGateway IngressType = "gateway"
)

// ForgeSpec points dotvirt at its git forge and the platform-tier repo. The forge
// credential here is the INSTALL-TIME admin token the operator uses to create the
// platform repo, distinct from (and more privileged than) dotvirt's runtime
// clone/push token, preserving the install-provisioner vs runtime-owns-nothing split.
type ForgeSpec struct {
	// URL is the forge base URL. Leave empty with managed on OpenShift: the operator
	// exposes Forgejo on a router-assigned host and reports it in status.forgeURL. Set it
	// for a bring-your-own forge, or to pin the managed forge's host.
	URL string `json:"url,omitempty"`
	// PlatformRepo is the cluster-scoped + tenancy repo (CUDN/NNCP/Namespace). The
	// operator ensures it exists; dotvirt routes platform creates here by kind. Defaults
	// to <forge URL>/dotvirt/platform.git for a managed forge.
	PlatformRepo string `json:"platformRepo,omitempty"`
	// Managed deploys a self-hosted Forgejo for evaluation; false = bring your own.
	Managed bool `json:"managed,omitempty"`
	// CredentialsSecret names a Secret holding the forge-admin credential (keys: url,
	// username, token). For a managed forge the operator WRITES this secret; point it at
	// an existing secret only for a bring-your-own forge.
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// InsecureTLS skips TLS verification when calling the forge API (a self-signed forge
	// Route, e.g. the bundled Forgejo). DEV/EVAL ONLY; never enable against a forge with
	// a trusted certificate.
	InsecureTLS bool `json:"insecureTLS,omitempty"`
}

// ArgoCDSpec locates the ArgoCD install dotvirt rides. Defaults suit OpenShift
// GitOps (openshift-gitops); override for community ArgoCD (argocd /
// argocd-application-controller). The operator binds the apply RBAC + AppProjects
// to this controller ServiceAccount.
// +kubebuilder:validation:XValidation:rule="!has(self.tenantKinds) || self.tenantKinds != 'Any' || !has(self.syncImpersonation) || !has(self.syncImpersonation.enabled) || self.syncImpersonation.enabled",message="tenantKinds Any needs sync impersonation: the project's sync account is the only ceiling on what its repo applies"
type ArgoCDSpec struct {
	// Namespace is where the ArgoCD instance runs; empty = the platform default
	// (openshift-gitops on OpenShift, argocd elsewhere).
	Namespace string `json:"namespace,omitempty"`
	// ControllerServiceAccount is the application-controller ServiceAccount the
	// apply RBAC and AppProjects bind to; empty = the platform default.
	ControllerServiceAccount string `json:"controllerServiceAccount,omitempty"`
	// ServerURL is the externally reachable ArgoCD base URL the forge posts webhooks
	// to (.../api/webhook) for instant sync. Empty = discover the OpenShift GitOps
	// server Route; if neither resolves, the webhook is skipped (Argo falls back to
	// its poll).
	ServerURL string `json:"serverURL,omitempty"`
	// SyncImpersonation makes ArgoCD sync each dotvirt Application as its
	// project's own ServiceAccount, which holds exactly the project's grants,
	// instead of the shared controller's account. On by default.
	SyncImpersonation SyncImpersonationSpec `json:"syncImpersonation,omitempty"`
	// TenantKinds is what a tenant repo may apply. Listed, the default: the
	// kinds dotvirt has a UI for. Any: every namespaced kind but the denied
	// ones (grants, quotas, the platform tier's kinds, OLM, KubeVirt's own
	// configuration, and TenantKindsDenied). Any holds only while sync
	// impersonation is on, the project's sync account then being the ceiling;
	// until then tenant repos keep the listed kinds.
	// +kubebuilder:validation:Enum=Listed;Any
	// +optional
	TenantKinds string `json:"tenantKinds,omitempty"`
	// TenantKindsDenied adds kinds no tenant repo may apply under Any; it never
	// removes one. Kind "*" denies the whole group. List here every operator
	// the cluster runs that acts with its own rights on a namespaced request
	// that can name another namespace or a cluster-wide resource: a tenant
	// repo could otherwise reach past its project through it.
	// +optional
	TenantKindsDenied []GroupKind `json:"tenantKindsDenied,omitempty"`
}

// AnyTenantKinds reports whether tenant repos are asked to apply any
// namespaced kind but the denied ones.
func (a ArgoCDSpec) AnyTenantKinds() bool { return a.TenantKinds == "Any" }

// GroupKind names a kind by API group ("" for the core group); kind "*" is the
// whole group.
type GroupKind struct {
	Group string `json:"group"`
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
}

// SyncImpersonationSpec is the ArgoCD instance's Argo CD sync impersonation
// (application.sync.impersonation.enabled).
type SyncImpersonationSpec struct {
	// Enabled, true when unset, turns impersonation on once every project's sync
	// account holds its grants and nothing else on the instance would stop
	// syncing; the SyncImpersonationReady condition says what is missing. The
	// setting is instance-wide: every Application on that ArgoCD then syncs as
	// the ServiceAccount its AppProject names, or not at all. false removes the
	// setting.
	Enabled *bool `json:"enabled,omitempty"`
	// IgnoreForeignApplications turns impersonation on although the instance
	// also syncs Applications outside dotvirt's AppProjects. Those fail to sync
	// until their AppProjects name a ServiceAccount (destinationServiceAccounts).
	IgnoreForeignApplications bool `json:"ignoreForeignApplications,omitempty"`
}

// On reports whether impersonation is asked for: unset is. A tenant repo should
// apply only what its project may without anyone having to ask for that.
func (s SyncImpersonationSpec) On() bool { return s.Enabled == nil || *s.Enabled }

// IngressSpec controls how the UI is exposed.
// +kubebuilder:validation:XValidation:rule="!has(self.type) || self.type != 'gateway' || has(self.gateway)",message="type gateway needs spec.ingress.gateway"
// +kubebuilder:validation:XValidation:rule="!has(self.gateway) || (has(self.host) && size(self.host) > 0)",message="a Gateway serves named hosts: set spec.ingress.host"
type IngressSpec struct {
	Type IngressType `json:"type,omitempty"`
	// Host is the external hostname the UI is served on. Leave empty on OpenShift for a
	// router-assigned host (reported in status.consoleURL); required for an Ingress on
	// vanilla Kubernetes and for a Gateway.
	Host string `json:"host,omitempty"`
	// Gateway is what serves the UI and a managed forge, through HTTPRoutes. Its
	// listener terminates TLS: the console URL is https. Name one where no router
	// serves Routes, such as MicroShift with its ingress Removed.
	Gateway *GatewayRef `json:"gateway,omitempty"`
}

// GatewayRef names the Gateway API Gateway the HTTPRoutes attach to.
type GatewayRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace of the Gateway; empty is this Dotvirt's. Its listener must allow
	// routes from the Dotvirt's namespace and no other: dotvirt's HTTPRoute
	// matches every path, so a route another namespace attaches on the same
	// hostname with a longer path takes that path, /api/login included.
	Namespace string `json:"namespace,omitempty"`
	// SectionName attaches to one listener; empty attaches to every listener
	// that admits the hostname.
	SectionName string `json:"sectionName,omitempty"`
}

// MetricsSpec points the Performance tab at a Prometheus/Thanos query API; empty
// disables it.
type MetricsSpec struct {
	// URL is a Prometheus-compatible query API base URL (for OpenShift, the
	// cluster Thanos querier service).
	URL string `json:"url,omitempty"`
}

// AuthSpec enables OpenShift SSO beside the always-present token login.
type AuthSpec struct {
	// OpenShiftSSO adds a "Sign in with OpenShift" button beside the token login. The
	// operator generates the client credential and, once the console host is assigned,
	// reports the exact OAuthClient to apply (redirect URI filled in) in
	// status.ssoOAuthClient. Registering that cluster-scoped OAuthClient stays a
	// cluster-admin act the operator deliberately doesn't perform. OpenShift only.
	OpenShiftSSO bool `json:"openShiftSSO,omitempty"`
}

// DotvirtSpec is the desired dotvirt install.
type DotvirtSpec struct {
	// Image is the dotvirt app image to deploy.
	Image   string      `json:"image,omitempty"`
	Forge   ForgeSpec   `json:"forge,omitempty"`
	ArgoCD  ArgoCDSpec  `json:"argocd,omitempty"`
	Ingress IngressSpec `json:"ingress,omitempty"`
	Metrics MetricsSpec `json:"metrics,omitempty"`
	Auth    AuthSpec    `json:"auth,omitempty"`
}

// DotvirtStatus is the observed install state.
type DotvirtStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is a short human-facing summary: Provisioning, BlockedOnDependencies or
	// Ready (the Phase* consts).
	Phase string `json:"phase,omitempty"`
	// Conditions follow the standard k8s conventions (see the Condition* consts).
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ForgeURL is the effective external forge base URL: the configured spec.forge.url,
	// or the host the operator assigned to a managed Forgejo when the URL was left empty.
	ForgeURL string `json:"forgeURL,omitempty"`
	// ConsoleURL is the external URL the dotvirt UI is served on; assigned by the
	// operator when spec.ingress.host is left empty on OpenShift.
	ConsoleURL string `json:"consoleURL,omitempty"`
	// SSOOAuthClient is a ready-to-apply command that registers the cluster-scoped
	// OAuthClient SSO needs, with the redirect URI filled from the assigned console host.
	// Set only while auth.openShiftSSO is on; run it once to finish SSO.
	SSOOAuthClient string `json:"ssoOAuthClient,omitempty"`
	// SyncImpersonationInstance is the ArgoCD resource, as namespace/name, the
	// operator set application.sync.impersonation.enabled on: it removes the
	// setting there when spec.argocd moves to another instance and before the
	// install is deleted, so no Application is left needing an account dotvirt
	// no longer maps.
	SyncImpersonationInstance string `json:"syncImpersonationInstance,omitempty"`
	// ForgeAdminHint is the command that reveals the managed Forgejo bootstrap admin
	// password (user dotvirt-bot); the value stays in the Secret, never in status.
	// Set only for a managed forge.
	ForgeAdminHint string `json:"forgeAdminHint,omitempty"`
}

// Dotvirt is one dotvirt install. Namespaced singleton in the operator's namespace;
// the operator itself holds the cluster RBAC to provision cluster-scoped resources.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=dv
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Console",type=string,JSONPath=`.status.consoleURL`
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
