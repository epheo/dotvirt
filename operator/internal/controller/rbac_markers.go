package controller

// The operator's OWN least-privilege RBAC (generated into config/rbac/role.yaml). Verbs
// are exactly what the controller's client does: install.Apply is server-side-apply, i.e.
// create+patch (never update); only the kinds it actually reads (dotvirts, secrets,
// deployments, routes) get list+watch (the cache); cleanup's DeleteAllOf is the
// `deletecollection` verb. The operator does NOT author ClusterRoles — it only `bind`s the
// three static operand roles — so it needs no `escalate` and no ClusterRole/RoleBinding writes.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/status,verbs=get;update;patch
// dotvirts/finalizers: unused on default clusters (AddFinalizer writes the main
// resource), but SetControllerReference sets blockOwnerDeletion, which the
// OwnerReferencesPermissionEnforcement admission plugin checks via this subresource.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;deletecollection
// configmaps get/update: read the default ingress CA (openshift-config-managed) and
// merge the managed forge's host into argocd-tls-certs-cm, so Argo VERIFIES the
// router-served cert instead of x509-failing on every repo.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;update;patch;deletecollection
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts;persistentvolumeclaims,verbs=create;patch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;patch
// routes/custom-host: required to set an explicit spec.host on a Route (the forge + app
// exposure hosts). `update` on top of `create` so editing spec.ingress.host / spec.forge.url
// on a live CR re-homes the existing Route instead of being denied ("cannot set host field").
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes/custom-host,verbs=create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects;applications;applicationsets,verbs=create;patch;deletecollection
// clusterrolebindings: the operator creates the bindings that wire the static operand roles
// to the dotvirt SA / Argo controller / platform-admins, and DeleteAllOf-cleans them up.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create;patch;deletecollection
// clusterroles `bind`: the operator's ONLY rbac-authoring right — bind these three named
// static roles into the bindings above. No escalate, no role create/update/delete.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-platform-network-admin,verbs=bind
