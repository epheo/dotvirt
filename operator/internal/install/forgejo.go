package install

import (
	"net/url"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// Managed (eval) Forgejo: deployed in the dotvirt namespace, single-replica, eval-grade.
// The operator talks to it in-cluster via its Service; on OpenShift it also gets a Route
// (an explicit spec.forge.url host, or a router-assigned one when the URL is left empty)
// so its UI and PRs are reviewable off-cluster. The bootstrap was verified live against a
// real Forgejo (see the initContainer below). For production, bring your own forge instead.
const (
	// Pinned by digest so the eval forge is reproducible and declarable in the CSV's
	// relatedImages. No bot re-pins it: hack/check-pins.sh fails the nightly scan when
	// the tag moves or a newer major ships. The ROOTLESS image runs non-root and
	// admits to OpenShift's restricted-v2 SCC with no anyuid grant (verified live: an
	// arbitrary injected UID with gid 0 completes migrate + admin-create + serve), so
	// it carries dotvirt's standard hardened securityContext like the operand does.
	forgejoImage       = "codeberg.org/forgejo/forgejo:17-rootless@sha256:39362ab93a94c0c9b8b330d2a877ddbb3f02fa8c0cba1b8d686d437b39f36a04"
	forgejoSAName      = "dotvirt-forgejo"
	ForgejoAdminSecret = "dotvirt-forgejo-admin" // generated admin password, under ForgejoAdminKey
	ForgejoAdminKey    = "password"
	forgejoPVCName     = "dotvirt-forgejo-data"
	ForgejoServiceName = "dotvirt-forgejo"
	ForgejoBotUser     = "dotvirt-bot" // the service user the operator mints a token for
)

// ForgejoHTTPPort is the managed Forgejo's single HTTP port - the one source for
// its Service, the container port and probes, and every URL/exposure built to it.
const ForgejoHTTPPort int32 = 3000

var forgejoSelector = map[string]string{"app": ForgejoServiceName}

// ForgejoServiceURL is the in-cluster base URL of the managed Forgejo.
func ForgejoServiceURL(dv *dotvirtv1alpha1.Dotvirt) string {
	return svcURL(ForgejoServiceName, dv.Namespace, ForgejoHTTPPort)
}

// forgejoExternalURL is the browser/clone-facing base URL: spec.forge.webURL, else the
// effective spec.forge.url (the operator fills a derived one from the router-assigned
// Route before rendering), else the in-cluster Service URL, the fallback only a dry-run
// or a still-unresolved host hits, since a real render resolves the URL first.
// Forgejo answers on any host, so dotvirt and ArgoCD still reach it at spec.forge.url.
func forgejoExternalURL(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.WebURL != "" {
		return strings.TrimRight(dv.Spec.Forge.WebURL, "/")
	}
	if dv.Spec.Forge.URL != "" {
		return strings.TrimRight(dv.Spec.Forge.URL, "/")
	}
	return ForgejoServiceURL(dv)
}

// ForgejoWebHost is the host the managed Forgejo's exposure serves:
// spec.forge.url's, or spec.forge.webURL's when that URL is in-cluster. A
// webURL beside a public url is an app of the admin's fronting the forge,
// whose host the forge never claims. "" for an in-cluster host, or none.
func ForgejoWebHost(dv *dotvirtv1alpha1.Dotvirt) string {
	for _, raw := range []string{dv.Spec.Forge.URL, dv.Spec.Forge.WebURL} {
		if u, err := url.Parse(raw); raw != "" && err == nil && !InClusterHost(u.Hostname()) {
			return u.Hostname()
		}
	}
	return ""
}

// InClusterHost reports a host only cluster DNS resolves: a bare Service
// name, or <service>.<namespace>.svc[.<cluster domain>].
func InClusterHost(host string) bool {
	labels := strings.Split(host, ".")
	return len(labels) == 1 || len(labels) >= 3 && labels[2] == "svc"
}

// ForgeGateway is the Gateway a managed forge's HTTPRoute attaches to:
// spec.forge.gateway, else spec.ingress.gateway.
func ForgeGateway(dv *dotvirtv1alpha1.Dotvirt) *dotvirtv1alpha1.GatewayRef {
	if g := dv.Spec.Forge.Gateway; g != nil {
		return g
	}
	return dv.Spec.Ingress.Gateway
}

// ForgejoHost is spec.forge.url's hostname, the one ArgoCD clones from.
func ForgejoHost(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.URL == "" {
		return ""
	}
	if u, err := url.Parse(dv.Spec.Forge.URL); err == nil {
		// Hostname, never Host: a URL carrying a port (an in-cluster service
		// URL like http://forge.ns.svc:3000) would otherwise render a Route
		// whose spec.host the apiserver rejects, wedging ForgeReady.
		return u.Hostname()
	}
	return ""
}

// ForgejoServiceAccount runs the Forgejo pod under dotvirt's hardened, non-root
// securityContext - no SCC grant required (the rootless image needs none).
func ForgejoServiceAccount(dv *dotvirtv1alpha1.Dotvirt) *corev1.ServiceAccount {
	return serviceAccount(forgejoSAName, dv)
}

// ForgejoPVC holds Forgejo's data. NOT owner-referenced by the caller - orphaned on
// uninstall so the platform repo's git data survives (the operator owns Forgejo's
// lifecycle, not its data).
func ForgejoPVC(dv *dotvirtv1alpha1.Dotvirt) *corev1.PersistentVolumeClaim {
	return pvc(forgejoPVCName, dv, "5Gi")
}

func ForgejoService(dv *dotvirtv1alpha1.Dotvirt) *corev1.Service {
	return service(ForgejoServiceName, dv, forgejoSelector, ForgejoHTTPPort)
}

// forgejoResources bounds the eval forge - modest single-replica sizing, shared by
// the bootstrap init and main containers.
func forgejoResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
}

// forgejoEnv is the config shared by the init + main containers. It does NOT override
// GITEA_CUSTOM/GITEA_WORK_DIR: the rootless image's defaults all live under
// /var/lib/gitea (the one PVC mount). Overriding to a custom path is what breaks the
// rootless image's arbitrary-UID writability - keep the defaults.
func forgejoEnv(dv *dotvirtv1alpha1.Dotvirt, argoWebhookHost string) []corev1.EnvVar {
	// dotvirt's webhook is delivered to its in-cluster Service; Forgejo's SSRF guard
	// blocks private targets by default, so allow that host - keeping `external` so
	// delivery to any public webhook still works. The ArgoCD webhook host must be
	// allowed by NAME on top of `external`: that entry matches only public resolved
	// IPs, and the Argo Route often resolves to a private ingress VIP (lab/on-prem
	// clusters), where the SSRF guard would silently drop every forge->Argo delivery.
	allowed := svcHost(AppName, dv.Namespace) + ",external"
	if argoWebhookHost != "" {
		allowed += "," + argoWebhookHost
	}
	env := []corev1.EnvVar{
		{Name: "FORGEJO__security__INSTALL_LOCK", Value: "true"},
		{Name: "FORGEJO__database__DB_TYPE", Value: "sqlite3"},
		{Name: "FORGEJO__server__ROOT_URL", Value: forgejoExternalURL(dv) + "/"},
		{Name: "FORGEJO__webhook__ALLOWED_HOST_LIST", Value: allowed},
		// The repos are created public, so without this the Route serves every
		// manifest anonymously. Every machine reader authenticates: the app and
		// ArgoCD clone with the bot token, the operator calls the API with it, and
		// /api/healthz sits outside the sign-in gate.
		{Name: "FORGEJO__service__REQUIRE_SIGNIN_VIEW", Value: "true"},
	}
	// Webhook TLS is VERIFIED: the mounted ingress CA joins the system pool via
	// Go's colon-separated SSL_CERT_DIR (empty dir on vanilla).
	certDirs := "/etc/ssl/certs:" + forgejoCADir
	if o := dv.Spec.Forge.OIDC; o != nil {
		// People sign in through the issuer alone, each to an account the forge
		// creates at their first sign-in and links to no existing one. The
		// password form is off: 17.0 links a new subject to a session already
		// signed in, whatever ACCOUNT_LINKING says. The bot's password keeps
		// the API (basic auth), which the operator mints its token through.
		env = append(env,
			corev1.EnvVar{Name: "FORGEJO__service__DISABLE_REGISTRATION", Value: "false"},
			corev1.EnvVar{Name: "FORGEJO__service__ALLOW_ONLY_EXTERNAL_REGISTRATION", Value: "true"},
			corev1.EnvVar{Name: "FORGEJO__service__ENABLE_INTERNAL_SIGNIN", Value: "false"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__ENABLE_AUTO_REGISTRATION", Value: "true"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__USERNAME", Value: "preferred_username"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__ACCOUNT_LINKING", Value: "disabled"},
		)
		if o.CA != nil {
			certDirs += ":" + forgejoOIDCCADir
		}
	} else {
		// The one account is the operator-minted bot; an exposed forge must not
		// mint more. Every setting the issuer turns is turned back: app.ini on
		// the volume keeps a key no env names any more.
		env = append(env,
			corev1.EnvVar{Name: "FORGEJO__service__DISABLE_REGISTRATION", Value: "true"},
			corev1.EnvVar{Name: "FORGEJO__service__ALLOW_ONLY_EXTERNAL_REGISTRATION", Value: "false"},
			corev1.EnvVar{Name: "FORGEJO__service__ENABLE_INTERNAL_SIGNIN", Value: "true"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__ENABLE_AUTO_REGISTRATION", Value: "false"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__USERNAME", Value: "nickname"},
			corev1.EnvVar{Name: "FORGEJO__oauth2_client__ACCOUNT_LINKING", Value: "login"},
		)
	}
	return append(env, corev1.EnvVar{Name: "SSL_CERT_DIR", Value: certDirs})
}

// forgejoOIDCEnv passes spec.forge.oidc to the bootstrap, which registers the
// issuer; none when unset.
func forgejoOIDCEnv(o *dotvirtv1alpha1.ForgeOIDCSpec) []corev1.EnvVar {
	if o == nil {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "FORGE_OIDC_NAME", Value: o.ForgeOIDCName()},
		{Name: "FORGE_OIDC_ISSUER", Value: o.IssuerURL},
		{Name: "FORGE_OIDC_CLIENT_ID", Value: o.ClientID},
		secretEnv("FORGE_OIDC_SECRET", o.ClientSecret.Name, o.ClientSecret.Key, false),
		{Name: "FORGE_OIDC_SCOPES", Value: strings.Join(o.Scopes, " ")},
		{Name: "FORGE_OIDC_GROUP_CLAIM", Value: o.GroupClaimName},
		{Name: "FORGE_OIDC_ADMIN_GROUP", Value: o.AdminGroup},
	}
}

// forgejoCADir and forgejoOIDCCADir are where the ingress CA and the forge's
// issuer CA mount into both Forgejo containers, joining the system trust via
// SSL_CERT_DIR.
const (
	forgejoCADir     = "/var/run/dotvirt/ca"
	forgejoOIDCCADir = "/var/run/dotvirt/oidc-ca"
)

// ForgejoDeployment runs the rootless Forgejo with a one-shot bootstrap initContainer
// that migrates the DB and reconciles the admin service user's password to the current
// secret on every start (create on a fresh volume, change-password on an existing one).
// That sequence was verified live as an arbitrary OpenShift-injected UID. The main
// container then serves on the prepared data. The operator mints the API token
// afterward (it can't exec).
//
// No chown/su-exec: the container already runs as the unprivileged user, and the data
// dir is group-writable (gid 0 on OpenShift via the SCC; fsGroup on vanilla K8s). The
// PVC mounts at the image's default GITEA_WORK_DIR (/var/lib/gitea), its one declared
// volume, which holds app.ini too.
func ForgejoDeployment(dv *dotvirtv1alpha1.Dotvirt, setFSGroup bool, argoWebhookHost, adminSecretHash string) *appsv1.Deployment {
	replicas := int32(1)
	forgejoImg := imageFromEnv("RELATED_IMAGE_FORGEJO", forgejoImage)
	dataMount := corev1.VolumeMount{Name: "data", MountPath: "/var/lib/gitea"}
	caMount := corev1.VolumeMount{Name: "ingress-ca", MountPath: forgejoCADir, ReadOnly: true}
	adminPW := corev1.EnvVar{Name: "ADMIN_PW", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ForgejoAdminSecret}, Key: ForgejoAdminKey,
		},
	}}
	// environment-to-ini renders app.ini from the FORGEJO__* env (the rootless image's
	// entrypoint normally does this; we override the command, so run it ourselves).
	// migrate then has a config to load. No chown/su-exec: already the non-root user.
	//
	// create-or-reset: on a fresh volume `user create` seeds the admin; on an existing
	// volume it fails (user exists) and change-password reconciles the DB password to the
	// current admin secret. So every pod start converges the password, and a regenerated
	// admin secret (a CR delete/recreate over the retained data PVC, or a manual secret
	// delete) can never leave the operator's basic-auth mint stuck on a stale password.
	// The initContainer has local DB access, so it needs no prior password to reset it.
	//
	// The issuer (spec.forge.oidc): registering it reads the issuer's discovery
	// document, so an issuer down would keep the forge from starting. It runs
	// only when its settings change, which a digest on the volume records;
	// update-oauth changes only the flags given, so each run passes them all.
	// Removing spec.forge.oidc deletes the source the digest names, which the
	// forge refuses once accounts came from it: it then stays, and says so.
	bootstrap := `set -ef
mkdir -p "$(dirname "$GITEA_APP_INI")"
environment-to-ini
forgejo migrate
forgejo admin user create --admin --username ` + ForgejoBotUser +
		` --password "$ADMIN_PW" --email ` + ForgejoBotUser + `@dotvirt.local --must-change-password=false ` +
		`|| forgejo admin user change-password --username ` + ForgejoBotUser + ` --password "$ADMIN_PW" --must-change-password=false
mark=/var/lib/gitea/dotvirt-oidc
source_id() { forgejo admin auth list | awk -F '\t' -v n="$1" 'NR > 1 && $2 == n { print $1 }'; }
if [ -n "${FORGE_OIDC_ISSUER:-}" ]; then
  sum="$(printf '%s\n' "$FORGE_OIDC_NAME" "$FORGE_OIDC_ISSUER" "$FORGE_OIDC_CLIENT_ID" "$FORGE_OIDC_SECRET" "$FORGE_OIDC_SCOPES" \
    "$FORGE_OIDC_GROUP_CLAIM" "$FORGE_OIDC_ADMIN_GROUP" | sha256sum | cut -d ' ' -f 1)"
  old=""
  [ ! -s "$mark" ] || old="$(cat "$mark")"
  if [ "$old" != "$FORGE_OIDC_NAME $sum" ]; then
    set -- --name "$FORGE_OIDC_NAME" --provider openidConnect --key "$FORGE_OIDC_CLIENT_ID" --secret "$FORGE_OIDC_SECRET" \
      --auto-discover-url "${FORGE_OIDC_ISSUER%/}/.well-known/openid-configuration" --scopes openid \
      --group-claim-name "$FORGE_OIDC_GROUP_CLAIM" --admin-group "$FORGE_OIDC_ADMIN_GROUP" --skip-local-2fa=false
    for scope in $FORGE_OIDC_SCOPES; do set -- "$@" --scopes "$scope"; done
    old="${old%% *}"
    id="$(source_id "${old:-$FORGE_OIDC_NAME}")"
    if [ -n "$id" ]; then forgejo admin auth update-oauth --id "$id" "$@"; else forgejo admin auth add-oauth "$@"; fi
    printf '%s %s\n' "$FORGE_OIDC_NAME" "$sum" > "$mark"
  fi
elif [ -s "$mark" ]; then
  id="$(source_id "$(cut -d ' ' -f 1 "$mark")")"
  if [ -n "$id" ] && ! forgejo admin auth delete --id "$id"; then
    echo "the forge keeps the issuer: accounts came from it; remove them, or revoke its client at the issuer" >&2
  fi
  rm -f "$mark"
fi`

	caOptional := true
	mounts := []corev1.VolumeMount{dataMount, caMount}
	var oidcCA []corev1.Volume
	if o := dv.Spec.Forge.OIDC; o != nil && o.CA != nil {
		// Optional: a missing CA leaves the forge up, its issuer failing verification.
		mounts = append(mounts, corev1.VolumeMount{Name: "oidc-ca", MountPath: forgejoOIDCCADir, ReadOnly: true})
		oidcCA = append(oidcCA, corev1.Volume{Name: "oidc-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: o.CA.Name},
			Items:                []corev1.KeyToPath{{Key: o.CA.Key, Path: "ca.crt"}},
			Optional:             &caOptional,
		}}})
	}
	noAutomount := false
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(ForgejoServiceName, dv),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, // RWO data
			Selector: &metav1.LabelSelector{MatchLabels: forgejoSelector},
			Template: corev1.PodTemplateSpec{
				// A rotated admin secret must roll the pod so the initContainer
				// reconciles the DB password; without this, rotation waits on a
				// human runbook.
				ObjectMeta: metav1.ObjectMeta{
					Labels:      forgejoSelector,
					Annotations: map[string]string{"dotvirt.io/admin-secret-hash": adminSecretHash},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: forgejoSAName,
					// Forgejo never calls the Kubernetes API.
					AutomountServiceAccountToken: &noAutomount,
					SecurityContext:              hardenedPodSecurityContext(setFSGroup),
					InitContainers: []corev1.Container{{
						Name:            "bootstrap",
						Image:           forgejoImg,
						Command:         []string{"sh", "-c", bootstrap},
						Env:             append(append(forgejoEnv(dv, argoWebhookHost), adminPW), forgejoOIDCEnv(dv.Spec.Forge.OIDC)...),
						VolumeMounts:    mounts,
						Resources:       forgejoResources(),
						SecurityContext: hardenedContainerSecurityContext(),
					}},
					Containers: []corev1.Container{{
						Name:            "forgejo",
						Image:           forgejoImg,
						Env:             forgejoEnv(dv, argoWebhookHost),
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: ForgejoHTTPPort}},
						VolumeMounts:    mounts,
						ReadinessProbe:  healthProbe(ForgejoHTTPPort, 8, 5, 0),
						LivenessProbe:   healthProbe(ForgejoHTTPPort, 30, 20, 5),
						Resources:       forgejoResources(),
						SecurityContext: hardenedContainerSecurityContext(),
					}},
					Volumes: append([]corev1.Volume{
						{
							Name:         "data",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: forgejoPVCName}},
						},
						// Optional: absent on vanilla; the trust dir is then just empty.
						{Name: "ingress-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: IngressCAConfigMap}, Optional: &caOptional,
						}}},
					}, oidcCA...),
				},
			},
		},
	}
}
