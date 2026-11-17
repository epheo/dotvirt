// Package adminobjects is what an install needs but its operator may not
// create. Each object is rendered from typed Params, which the console takes
// from its own configuration and from reads under the cluster admin's token,
// never from anything the operator writes: an operator that could choose what
// an admin applies could have itself granted anything. The operator renders
// the same objects only to report whether they are in place.
package adminobjects

import (
	"fmt"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// State is how a live object compares with its render.
type State string

const (
	NotInstalled State = "NotInstalled"
	// Outdated: rendered for other parameters or by another version, or
	// changed after it was installed.
	Outdated  State = "Outdated"
	Installed State = "Installed"
	// Stale: an object the install no longer asks for, still there (Leftovers).
	Stale State = "Stale"
)

// Object is one rendered object and what it is for.
type Object struct {
	Purpose string
	Object  *unstructured.Unstructured
}

// OperatorDeployment is the operator's own Deployment, in the operator's
// namespace: OLM or GitOps writes it, the operator cannot.
const OperatorDeployment = "dotvirt-operator-controller"

// Params is everything the admin objects are rendered from.
type Params struct {
	// Operator is the operator's exact username,
	// system:serviceaccount:<namespace>:<name>.
	Operator string
	// Namespace is the install's (the Dotvirt resource's) namespace.
	Namespace string
	// ArgoNamespace and ArgoController are the ArgoCD namespace the install
	// writes into and its application controller's ServiceAccount.
	ArgoNamespace, ArgoController string
	// The images an admin chose beside the official repositories, "" when
	// unset: the Dotvirt resource's spec.image, and the operator Deployment's
	// RELATED_IMAGE_DOTVIRT and RELATED_IMAGE_FORGEJO.
	SpecImage, DotvirtImage, ForgejoImage string
	// InsecureTLS is the admin's spec.forge.insecureTLS: the console may skip
	// TLS verification only when it is set.
	InsecureTLS bool
	// Metrics is the admin's spec.metrics as rendered (MetricsFor).
	Metrics Metrics
	// ConsoleHost is the host the console is reached by (ConsoleHost), ""
	// when nothing exposes it.
	ConsoleHost string
	// BrandingConfigMap, MetricsCAConfigMap and ForgeSecret are the volumes
	// an admin chose for the console (spec.branding.configMap,
	// spec.metrics.ca.name, ForgeSecret).
	BrandingConfigMap, MetricsCAConfigMap, ForgeSecret string
	// OIDCSecret and OIDCSecretKey are spec.auth.oidc.clientSecret, and
	// OIDCIssuer spec.auth.oidc.issuerURL.
	OIDCSecret, OIDCSecretKey, OIDCIssuer string
	// ForgeManaged is spec.forge.managed: a forge the operator runs, whose
	// credential it mints. Otherwise the credential is an admin's, and the
	// console sends it only to the URL its Secret names.
	ForgeManaged bool
	// InstallName and InstallUID are the install resource's, which owns what
	// is rendered for it alone (CredentialGrant).
	InstallName string
	InstallUID  types.UID
}

// Valid reports whether every pin is known and well formed: a render missing
// one would pin nothing.
func (p Params) Valid() bool { return p.Malformed() == "" }

// Malformed names the first pin that is not well formed, "" when none is;
// never its value, which may name an admin's Secret.
func (p Params) Malformed() string {
	name := func(s string) bool { return len(validation.IsDNS1123Subdomain(s)) == 0 }
	optional := func(s string) bool { return s == "" || name(s) }
	for _, c := range []struct {
		pin string
		ok  bool
	}{
		{"operator", ValidUsername(p.Operator)},
		{"install namespace", len(validation.IsDNS1123Label(p.Namespace)) == 0},
		{"ArgoCD namespace", len(validation.IsDNS1123Label(p.ArgoNamespace)) == 0},
		{"ArgoCD controller", name(p.ArgoController)},
		{"forge Secret", name(p.ForgeSecret)},
		{"branding ConfigMap", optional(p.BrandingConfigMap)},
		{"metrics CA ConfigMap", optional(p.MetricsCAConfigMap)},
		{"console host", optional(p.ConsoleHost)},
		{"OIDC client Secret", optional(p.OIDCSecret)},
		{"OIDC client Secret key", p.OIDCSecretKey == "" || len(validation.IsConfigMapKey(p.OIDCSecretKey)) == 0},
		{"OIDC issuer", p.OIDCIssuer == "" || httpsURL(p.OIDCIssuer)},
	} {
		if !c.ok {
			return c.pin
		}
	}
	return ""
}

// OperatorNamespace is the namespace of p.Operator's ServiceAccount.
func (p Params) OperatorNamespace() string {
	parts := strings.Split(p.Operator, ":")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

// ValidUsername reports whether u is exactly
// system:serviceaccount:<namespace>:<name>.
func ValidUsername(u string) bool {
	parts := strings.Split(u, ":")
	return len(parts) == 4 && parts[0] == "system" && parts[1] == "serviceaccount" &&
		len(validation.IsDNS1123Label(parts[2])) == 0 && len(validation.IsDNS1123Subdomain(parts[3])) == 0
}

// Render is every admin object an install asks for, rendered from p: the
// guard, and the forge credential's grant when the credential is an admin's
// Secret the operator's role does not name. Append here to ask for more, and
// to Allowed and diff.
func Render(p Params) []Object {
	return append(Guard(p), credentialObjects(p)...)
}

// Ref names one object an install may ask an admin to apply.
type Ref struct {
	Group, Version, Kind, Resource string
	// Namespace is "" for a cluster-scoped kind.
	Namespace, Name string
}

// GVR is the resource the object is applied as.
func (r Ref) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

// Allowed is every object an install in installNS may ask a cluster admin to
// apply: the console applies nothing else.
func Allowed(installNS string) []Ref {
	var out []Ref
	for _, name := range GuardNames {
		out = append(out,
			Ref{"admissionregistration.k8s.io", "v1", "ValidatingAdmissionPolicy", "validatingadmissionpolicies", "", name},
			Ref{"admissionregistration.k8s.io", "v1", "ValidatingAdmissionPolicyBinding", "validatingadmissionpolicybindings", "", name})
	}
	return append(out,
		Ref{"rbac.authorization.k8s.io", "v1", "Role", "roles", installNS, CredentialGrantName},
		Ref{"rbac.authorization.k8s.io", "v1", "RoleBinding", "rolebindings", installNS, CredentialGrantName})
}

// Allow is the entry of allowed o is, by group, version, kind, namespace and
// name, and whether there is one.
func Allow(allowed []Ref, o *unstructured.Unstructured) (Ref, bool) {
	gv, err := schema.ParseGroupVersion(o.GetAPIVersion())
	if err != nil {
		return Ref{}, false
	}
	for _, r := range allowed {
		if r.Group == gv.Group && r.Version == gv.Version && r.Kind == o.GetKind() && r.Namespace == o.GetNamespace() && r.Name == o.GetName() {
			return r, true
		}
	}
	return Ref{}, false
}

// diff names how a live object differs from its render in what decides its
// effect, ignoring apiserver defaults; "" when it does not. Every kind in
// Allowed has one.
var diff = map[schema.GroupKind]func(want, live *unstructured.Unstructured) string{
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy"}:        diffPolicy,
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicyBinding"}: diffBinding,
	{Group: "rbac.authorization.k8s.io", Kind: "Role"}:                                diffRole,
	{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}:                         diffRoleBinding,
}

// Compare weighs live, nil when absent, against want. detail says what
// differs, so an install over it is a choice.
func Compare(want, live *unstructured.Unstructured) (state State, detail string) {
	what := want.GetKind() + " " + want.GetName()
	if live == nil {
		return NotInstalled, what + " is missing"
	}
	d, ok := diff[want.GroupVersionKind().GroupKind()]
	if !ok {
		return Outdated, fmt.Sprintf("%s: no comparer for its kind", what)
	}
	if why := d(want, live); why != "" {
		return Outdated, what + " " + why
	}
	return Installed, ""
}

// ApplyCommand applies a manifest an admin saved to a file. It takes no data:
// the objects travel as the manifest, never through a shell. Without
// --force-conflicts, kubectl names any field someone else changed instead of
// taking it back; the admin decides.
const ApplyCommand = "kubectl apply --server-side --field-manager=dotvirt -f"

// httpsURL reports whether s is an absolute https URL, as the CRD admits an
// issuer.
func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}
