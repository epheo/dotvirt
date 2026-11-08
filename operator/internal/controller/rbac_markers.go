package controller

// The operator's OWN least-privilege RBAC (generated into config/rbac/role.yaml). Verbs
// are exactly what the controller's client does: install.Apply is server-side-apply, i.e.
// create+patch (never update); only the kinds it watches (dotvirts, secrets,
// deployments, routes, namespaces) get watch; prune's label-selected sweep is list on
// each rendered kind plus delete BY NAME (below). The operator does NOT author ClusterRoles - it only
// `bind`s the static operand roles - so it needs no `escalate` and no
// ClusterRole/RoleBinding writes.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/status,verbs=get;update;patch
// dotvirts/finalizers: unused on default clusters (AddFinalizer writes the main
// resource), but SetControllerReference sets blockOwnerDeletion, which the
// OwnerReferencesPermissionEnforcement admission plugin checks via this subresource.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;patch
// configmaps get/patch: read the default ingress CA (openshift-config-managed) and
// SSA-merge the managed forge's host into argocd-tls-certs-cm, so Argo VERIFIES the
// router-served cert instead of x509-failing on every repo.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;create;patch
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=list;create;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=list;create;patch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;patch
// routes/custom-host: required to set an explicit spec.host on a Route (the forge + app
// exposure hosts). `update` on top of `create` so editing spec.ingress.host / spec.forge.url
// on a live CR re-homes the existing Route instead of being denied ("cannot set host field").
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes/custom-host,verbs=create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=list;create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects;applications;applicationsets,verbs=list;create;patch
// namespaces list/watch: the project-labeled namespaces the tenant AppProjects are
// derived from (the cache selects on the project label; CacheByObject).
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=list;watch
// clusterrolebindings: the operator creates the bindings that wire the static operand roles
// to the dotvirt SA / Argo controller / platform-admins.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=list;create;patch
// clusterroles `bind`: the operator's ONLY rbac-authoring right - bind these named
// static roles into the bindings above. No escalate, no role create/update/delete.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-platform-network-admin;dotvirt-platform-sync,verbs=bind
// Sync impersonation: SubjectAccessReviews ask what each sync account may do before
// the operator turns it on, the RoleBindings the platform Application manages say
// which roles the platform account must bind (read point-wise, never listed or
// watched), and the operator sets that one key on the ArgoCD instance (get + patch on
// its two conventional names only).
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get
// +kubebuilder:rbac:groups=argoproj.io,resources=argocds,resourceNames=openshift-gitops;argocd,verbs=get;patch
//
// delete, scoped to the fixed names the install renders (prune and the finalizer
// delete one object at a time, never a collection): a cluster-wide grant would let
// the operator delete any Secret or binding in the cluster. A name missing here is
// skipped by prune and left behind; TestDeleteGrantCoversEveryRenderedName fails first.
// AppProjects are the one exception: one per tenant project, named after it, so no
// resourceNames list can cover them. The unscoped grant is bounded by what it reaches
// (ArgoCD permission sets, which Argo-admin RBAC guards and Argo itself needs to apply
// anything) and by prune, which deletes only AppProjects carrying this install's labels.
// +kubebuilder:rbac:groups=apps,resources=deployments,resourceNames=dotvirt;dotvirt-forgejo,verbs=delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,resourceNames=dotvirt;dotvirt-forgejo,verbs=delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies;ingresses,resourceNames=dotvirt;dotvirt-forgejo,verbs=delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,resourceNames=dotvirt;dotvirt-forgejo,verbs=delete
// +kubebuilder:rbac:groups="",resources=secrets,resourceNames=dotvirt-appset-plugin;dotvirt-repo-creds,verbs=delete
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=dotvirt-appset-plugin,verbs=delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-platform-network-admin;dotvirt-platform-sync,verbs=delete
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects,verbs=delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,resourceNames=dotvirt-platform,verbs=delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applicationsets,resourceNames=dotvirt-projects,verbs=delete
