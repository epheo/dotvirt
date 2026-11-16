package controller

// The operator's OWN least-privilege RBAC (generated into config/rbac/role.yaml).
// Verbs are exactly what the controller's client does, on the names the install
// renders: install.Apply is server-side apply, which the apiserver authorizes as
// create WITH the object's name (a POST create could not be scoped), so create,
// patch and delete name every object (TestApplyGrantNamesEveryRenderedObject),
// and prune reads each rendered name instead of listing. A rule without names
// says why on the line above it. The operator does NOT author ClusterRoles - it
// only `bind`s the static operand roles - so it needs no `escalate` and no
// ClusterRole writes.
//
// dotvirts: the CR, which may live in any namespace (the CSV installs AllNamespaces).
// patch, never update: the finalizer goes on and off by a merge patch of the
// finalizers alone, so the operator never sends the spec.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/status,verbs=get;update;patch
// dotvirts/finalizers: unused on default clusters (AddFinalizer writes the main
// resource), but SetControllerReference sets blockOwnerDeletion, which the
// OwnerReferencesPermissionEnforcement admission plugin checks via this subresource.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/finalizers,verbs=update
//
// The operand and the managed forge, in the install namespace.
// +kubebuilder:rbac:groups=apps,resources=deployments,resourceNames=dotvirt;dotvirt-forgejo,verbs=create;patch;delete
// deployments get/list/watch without names: the cache's Deployment informer (Owns),
// which selects on the instance label, a selector RBAC cannot express.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,resourceNames=dotvirt;dotvirt-forgejo,verbs=get;create;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,resourceNames=dotvirt-drafts;dotvirt-forgejo-data,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies;ingresses,resourceNames=dotvirt;dotvirt-forgejo,verbs=get;create;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,resourceNames=dotvirt;dotvirt-forgejo,verbs=get;create;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,resourceNames=dotvirt;dotvirt-forgejo,verbs=create;patch;delete
// routes get/list/watch without names: the cache's Route informer (Owns, instance
// label), and the GitOps server Route read for the Argo webhook URL.
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch
// routes/custom-host without names: required to set an explicit spec.host on a Route
// (the forge and app exposure hosts), checked by OpenShift's route admission; it only
// matters on a Route the operator may write, and those are named above. `update` on
// top of `create` so editing spec.ingress.host / spec.forge.url on a live CR re-homes
// the existing Route instead of being denied ("cannot set host field").
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes/custom-host,verbs=create;update
//
// Secrets: the generated keys and the forge credential in the install namespace,
// the ApplicationSet plugin token and repo credentials in the ArgoCD namespace, and
// the webhook key in argocd-secret (install.OperatorSecretNames). Never listed or
// watched: a Secret informer would need list and watch on every Secret in the
// cluster. A forge credential under another name is granted by a cluster admin,
// in the install namespace only (install.ForgeCredentialGrant).
// +kubebuilder:rbac:groups="",resources=secrets,resourceNames=dotvirt-session;dotvirt-appset-plugin;dotvirt-webhook;dotvirt-argo-webhook;dotvirt-oauth;dotvirt-forgejo-admin;dotvirt-forge;dotvirt-repo-creds;argocd-secret,verbs=get;create;patch
// +kubebuilder:rbac:groups="",resources=secrets,resourceNames=dotvirt-appset-plugin;dotvirt-repo-creds,verbs=delete
//
// ConfigMaps, all by name: the default ingress CA (openshift-config-managed) and its
// copies, the ApplicationSet plugin config, the managed forge's host in
// argocd-tls-certs-cm (so Argo VERIFIES the router-served cert instead of
// x509-failing on every repo), and Argo's settings, read only. argocd-cm is also
// watched (the cache selects it by name, which the apiserver authorizes with that
// name): sync impersonation turned on or off there re-renders what tenant repos may apply.
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=dotvirt-ingress-ca;dotvirt-service-ca;dotvirt-appset-plugin;argocd-tls-certs-cm,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=default-ingress-cert;argocd-cm;argocd-cmd-params-cm;dotvirt-appset-plugin,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=argocd-cm,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=dotvirt-appset-plugin,verbs=delete
//
// namespaces list/watch: the project-labeled namespaces the tenant AppProjects are
// derived from (the cache selects on the project label; CacheByObject).
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=list;watch
//
// Argo objects in the ArgoCD namespace: the platform Application and the
// ApplicationSet by name.
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,resourceNames=dotvirt-platform,verbs=get;create;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applicationsets,resourceNames=dotvirt-projects,verbs=get;create;patch;delete
// applications list without names: sync impersonation reads every Application the
// instance syncs (apps-in-any-namespace patterns are globs), and the stale-AppProject
// check lists dotvirt's project Applications.
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=list
// appprojects without names: one per tenant project, named after it, and RBAC has no
// prefix match. Prune lists them only in the ArgoCD namespaces it wrote and deletes
// only this install's labelled ones; the unscoped grant reaches ArgoCD permission
// sets, which Argo-admin RBAC guards and Argo itself needs to apply anything.
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects,verbs=list;create;patch;delete
//
// clusterrolebindings: the bindings that wire the static operand roles to the dotvirt
// SA / Argo controller / platform-admins / every authenticated user, by name.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-argocd-read;dotvirt-platform-network-admin;dotvirt-platform-sync;dotvirt-storage-catalog,verbs=get;create;patch;delete
// rolebindings: the one RoleBinding, dotvirt-sync-request in the Argo namespace, which
// keeps dotvirt's Application patch off every other namespace.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,resourceNames=dotvirt-sync-request,verbs=create;patch;delete
// rolebindings and roles list/watch on dotvirt-forge-credential alone (the cache
// selects them by name): a cluster admin granting or revoking the forge
// credential, in either order, triggers a pass. The operator never writes them.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,resourceNames=dotvirt-forge-credential,verbs=list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,resourceNames=dotvirt-forge-credential,verbs=get;list;watch
// clusterroles `bind`: the operator's ONLY rbac-authoring right - it permits binding
// these named static roles even where the operator does not hold their rules (RBAC's
// escalation check refuses a binding otherwise). No escalate, no role
// create/update/delete.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-argocd-read;dotvirt-platform-network-admin;dotvirt-platform-sync;dotvirt-storage-catalog;dotvirt-sync-request,verbs=bind
//
// Sync impersonation: SubjectAccessReviews ask what each sync account may do before
// the operator turns it on, the RoleBindings the platform Application manages say
// which roles the platform account must bind (read point-wise, never listed or
// watched; any name, since the platform repo names them), and the operator sets
// that one key on the ArgoCD instance (get + patch on its two conventional names only).
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// selfsubjectaccessreviews: before minting a managed forge's token into an
// admin-named credential, the operator asks whether it may store it.
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=selfsubjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get
// +kubebuilder:rbac:groups=argoproj.io,resources=argocds,resourceNames=openshift-gitops;argocd,verbs=get;patch
//
// The cluster's ingress domain, by name: the console's router host, which the
// operator guard pins as the console reads it.
// +kubebuilder:rbac:groups=config.openshift.io,resources=ingresses,resourceNames=cluster,verbs=get
// The operator guard (guard.go) is read by name to report it, never written: a
// cluster admin installs it, so the operator cannot loosen what bounds it.
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingadmissionpolicies;validatingadmissionpolicybindings,resourceNames=dotvirt-operator-secrets;dotvirt-operator-deployments;dotvirt-operator-bindings;dotvirt-operator-install;dotvirt-operator-frontdoor,verbs=get
