package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// refuseSecret answers every read and write of Secret name as the apiserver
// does for an account without the grant, while *refused holds.
func refuseSecret(name string, refused *bool) interceptor.Funcs {
	forbidden := func() error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, name, errors.New("no grant"))
	}
	return interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok && key.Name == name && *refused {
				return forbidden()
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*corev1.Secret); ok && obj.GetName() == name && *refused {
				return forbidden()
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
	}
}

// answerReviews answers the operator's SelfSubjectAccessReviews with allowed,
// recording what each asked in asked.
func answerReviews(allowed bool, asked *[]authorizationv1.ResourceAttributes) func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
	return answerReviewsBy(func(authorizationv1.ResourceAttributes) bool { return allowed }, asked)
}

// answerReviewsBy answers each SelfSubjectAccessReview with allow's verdict.
func answerReviewsBy(allow func(authorizationv1.ResourceAttributes) bool, asked *[]authorizationv1.ResourceAttributes) func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
	return func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		review, ok := obj.(*authorizationv1.SelfSubjectAccessReview)
		if !ok {
			return cl.Create(ctx, obj, opts...)
		}
		*asked = append(*asked, *review.Spec.ResourceAttributes)
		review.Status.Allowed = allow(*review.Spec.ResourceAttributes)
		return nil
	}
}

// fakeForgejo answers the managed forge's token mint, and a ready Forgejo and
// its admin Secret stand beside dv.
func fakeForgejo(t *testing.T, dv *dotvirtv1alpha1.Dotvirt) (*httptest.Server, []client.Object) {
	srv, objs, _ := countingForgejo(t, dv)
	return srv, objs
}

// countingForgejo is fakeForgejo counting the tokens it mints.
func countingForgejo(t *testing.T, dv *dotvirtv1alpha1.Dotvirt) (*httptest.Server, []client.Object, *atomic.Int64) {
	t.Helper()
	mints := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			mints.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sha1":"tok123"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/user"):
			// The minted token validates, so a later pass keeps it.
			_, _ = w.Write([]byte(`{"login":"dotvirt-bot"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, []client.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoServiceName, Namespace: dv.Namespace},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoAdminSecret, Namespace: dv.Namespace},
			Data:       map[string][]byte{"password": []byte("adminpw")},
		},
	}, mints
}

// A bring-your-own forge's credential the operator may not read: its ArgoCD
// tier, whose tenant repo refs resolve against the credential's url, waits
// before writing anything, so nothing it rendered before is pruned; the rest of
// the install runs. Once granted, the pass goes on.
func TestUngrantedCredentialWaitsOnlyWhatNeedsIt(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	repoCreds := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.RepoCredsName, Namespace: "argocd", Labels: install.Labels(dv)}}
	refused := true
	c := testBuilder(t).WithObjects(dv, creds, repoCreds).WithInterceptorFuncs(refuseSecret(creds.Name, &refused)).Build()
	r := newReconciler(c, depsOK)
	r.OperatorNamespace = "dotvirt-operator"

	// A grant can stay pending for days: the wait comes back at the drift
	// cadence, and the grant's RoleBinding is watched.
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != driftResync {
		t.Errorf("requeue = %v, want %v", res.RequeueAfter, driftResync)
	}
	got := getCR(t, c, dv)
	ready := cond(got, dotvirtv1alpha1.ConditionForgeCredentialReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "NotGranted" {
		t.Fatalf("ForgeCredentialReady = %+v, want False/NotGranted", ready)
	}
	credentialMessageNamesNothing(t, ready.Message, "get", "new projects")
	for _, ct := range []string{
		dotvirtv1alpha1.ConditionArgoReady, dotvirtv1alpha1.ConditionSyncImpersonationReady, dotvirtv1alpha1.ConditionForgeRepoReady,
		dotvirtv1alpha1.ConditionArgoWebhook, dotvirtv1alpha1.ConditionDotvirtWebhook,
	} {
		if c := cond(got, ct); c == nil || c.Status != metav1.ConditionUnknown || c.Reason != reasonCredentialWait {
			t.Errorf("%s = %+v, want Unknown/%s", ct, c, reasonCredentialWait)
		}
	}
	if c := cond(got, dotvirtv1alpha1.ConditionForgeReady); c == nil || c.Reason != reasonCredentialWait {
		t.Errorf("ForgeReady = %+v, want it waiting on the credential", c)
	}
	// The workload phase ran to its end: ArgoReady, after it, is the one waiting.
	if c := cond(got, dotvirtv1alpha1.ConditionWorkloadReady); c == nil || c.Reason == reasonCredentialWait {
		t.Errorf("WorkloadReady = %+v: the workload does not need the credential and must not wait", c)
	}
	if !exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("the operand was not applied")
	}
	if !exists(t, c, &corev1.Secret{}, "argocd", install.RepoCredsName) {
		t.Error("the waiting pass pruned the repo credentials Argo still clones with")
	}
	if exists(t, c, &corev1.Secret{}, "argocd", install.AppsetSecretName) || len(got.Status.ArgoNamespaces) != 0 {
		t.Error("the waiting ArgoCD tier wrote to the ArgoCD namespace")
	}

	refused = false
	reconcileOnce(t, r, dv)
	got = getCR(t, c, dv)
	if ready := cond(got, dotvirtv1alpha1.ConditionForgeCredentialReady); ready == nil || ready.Status != metav1.ConditionTrue {
		t.Errorf("granted, ForgeCredentialReady = %+v, want True", ready)
	}
	if c := cond(got, dotvirtv1alpha1.ConditionArgoReady); c == nil || c.Reason == reasonCredentialWait {
		t.Errorf("granted, ArgoReady = %+v, still waiting", c)
	}
}

// A managed forge's ArgoCD tier needs the credential for its repo credentials
// alone (its base is spec.forge.url): it goes on, keeping the copy Argo has,
// while no token is minted and the forge's own steps wait.
func TestManagedForgeTierGoesOnWithoutTheCredential(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.CredentialsSecret = "custom-forge"
	srv, objs := fakeForgejo(t, dv)
	repoCreds := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.RepoCredsName, Namespace: "argocd", Labels: install.Labels(dv)}}
	refused := true
	c := testBuilder(t).WithObjects(append(objs, dv, repoCreds)...).WithInterceptorFuncs(refuseSecret("custom-forge", &refused)).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if ready := cond(got, dotvirtv1alpha1.ConditionForgeCredentialReady); ready == nil || ready.Status != metav1.ConditionFalse ||
		!strings.Contains(ready.Message, "get, create, patch") {
		t.Errorf("ForgeCredentialReady = %+v, want False granting read and write", ready)
	}
	if c := cond(got, dotvirtv1alpha1.ConditionArgoReady); c == nil || c.Reason == reasonCredentialWait {
		t.Errorf("ArgoReady = %+v: a managed forge's tier does not need the credential", c)
	}
	if c := cond(got, dotvirtv1alpha1.ConditionForgeRepoReady); c == nil || c.Reason != reasonCredentialWait {
		t.Errorf("ForgeRepoReady = %+v, want it waiting on the credential", c)
	}
	if !exists(t, c, argoObj("ApplicationSet", "", "", nil), "argocd", install.ApplicationSetName) {
		t.Error("the ApplicationSet was not applied")
	}
	if !exists(t, c, &corev1.Secret{}, "argocd", install.RepoCredsName) {
		t.Error("prune took the repo credentials the tier could not rebuild")
	}
}

// A managed forge writes its minted token into the credential: a refused write
// is the grant missing too, and the rest of the install goes on.
func TestManagedForgeWaitsOnACredentialItMayNotWrite(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.CredentialsSecret = "custom-forge"
	srv, objs := fakeForgejo(t, dv)
	refused := true
	funcs := refuseSecret("custom-forge", &refused)
	// The read is allowed (the Secret is simply absent); only the write is
	// refused, though asked it read as allowed.
	funcs.Get = nil
	var asked []authorizationv1.ResourceAttributes
	funcs.Create = answerReviews(true, &asked)
	c := testBuilder(t).WithObjects(append(objs, dv)...).WithInterceptorFuncs(funcs).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	res, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv))
	if err != nil || res != nil {
		t.Fatalf("reconcileForge = %v, %v; want the pass to go on", res, err)
	}
	if ready := cond(dv, dotvirtv1alpha1.ConditionForgeCredentialReady); ready == nil || ready.Status != metav1.ConditionFalse || !strings.Contains(ready.Message, "read and write") {
		t.Errorf("ForgeCredentialReady = %+v, want False asking for read and write", ready)
	}
	if c := cond(dv, dotvirtv1alpha1.ConditionForgeReady); c == nil || c.Reason != reasonCredentialWait {
		t.Errorf("ForgeReady = %+v, want it waiting on the credential", c)
	}
}

// A credential named like one of the operator's own Secrets would have the
// minted token replace that Secret's keys: refused, never read or written.
func TestCredentialNamedLikeTheOperatorsOwnIsRefused(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.CredentialsSecret = install.SessionSecretName
	srv, objs := fakeForgejo(t, dv)
	session := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.SessionSecretName, Namespace: dv.Namespace},
		Data:       map[string][]byte{"secret": []byte("session-key")},
	}
	c := testBuilder(t).WithObjects(append(objs, dv, session)...).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	if res, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv)); err != nil || res != nil {
		t.Fatalf("reconcileForge = %v, %v; want the pass to go on", res, err)
	}
	if ready := cond(dv, dotvirtv1alpha1.ConditionForgeCredentialReady); ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "NameTaken" {
		t.Errorf("ForgeCredentialReady = %+v, want False/NameTaken", ready)
	}
	if got := getSecret(t, r, dv.Namespace, install.SessionSecretName); len(got.Data) != 1 || string(got.Data["secret"]) != "session-key" {
		t.Errorf("the session Secret now holds %v", got.Data)
	}
	if role, binding := install.ForgeCredentialGrant(dv, "dotvirt-operator"); role != nil || binding != nil {
		t.Error("a grant was rendered for a name the operator's own role covers")
	}
}

// A credential the operator's own role names needs no grant: the condition is
// absent, and one left from an earlier custom name goes.
func TestOwnCredentialNeedsNoGrant(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge.CredentialsSecret = "dotvirt-forge"
	dv.Status.Conditions = []metav1.Condition{{
		Type: dotvirtv1alpha1.ConditionForgeCredentialReady, Status: metav1.ConditionFalse, Reason: "NotGranted", LastTransitionTime: metav1.Now(),
	}}
	c := testBuilder(t).WithObjects(dv).WithStatusSubresource(dv).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeCredentialReady); got != nil {
		t.Errorf("ForgeCredentialReady = %+v for the operator's own credential", got)
	}
}

// A completed prune swept every recorded ArgoCD namespace, so status keeps the
// current one alone; one that failed keeps them all for the next try.
func TestArgoNamespacesStayRecordedUntilAPruneCompletes(t *testing.T) {
	dv := testCR()
	// Two moves (argo-a, then argo-b, then argocd) with no prune completed between.
	dv.Status.ArgoNamespaces = []string{"argo-a", "argo-b"}
	mine := install.Labels(dv)
	stale := []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetConfigMapName, Namespace: "argo-a", Labels: mine}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetConfigMapName, Namespace: "argo-b", Labels: mine}},
	}
	fail := true
	c := testBuilder(t).WithObjects(append(stale, dv)...).WithStatusSubresource(dv).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if fail && obj.GetNamespace() == "argo-b" {
				return errors.New("apiserver unavailable")
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}).Build()
	r := newReconciler(c, depsOK)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dv)}); err == nil {
		t.Fatal("a failed prune did not fail the pass")
	}
	if got := getCR(t, c, dv).Status.ArgoNamespaces; len(got) != 3 {
		t.Errorf("after a failed prune, status.argoNamespaces = %v, want argo-a, argo-b and argocd", got)
	}
	fail = false
	reconcileOnce(t, r, dv)
	for _, ns := range []string{"argo-a", "argo-b"} {
		if exists(t, c, &corev1.ConfigMap{}, ns, install.AppsetConfigMapName) {
			t.Errorf("%s was not swept", ns)
		}
	}
	if got := getCR(t, c, dv).Status.ArgoNamespaces; len(got) != 1 || got[0] != "argocd" {
		t.Errorf("after a completed prune, status.argoNamespaces = %v, want [argocd]", got)
	}
}

// A grant that landed but is still refused is the apiserver's RBAC caches
// lagging the new binding: no further event comes once they catch up, so the
// pass retries within a minute instead of the drift cadence, in either mode.
func TestLandedGrantStillRefusedRetriesSoon(t *testing.T) {
	binding := func(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.RoleBinding {
		_, b := install.ForgeCredentialGrant(dv, "dotvirt-operator")
		b.CreationTimestamp = metav1.Now()
		return b
	}
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	byo, creds := byoForgeCR(forgeDown.URL)
	refused := true
	c := testBuilder(t).WithObjects(byo, creds, binding(byo)).WithInterceptorFuncs(refuseSecret(creds.Name, &refused)).Build()
	if res := reconcileOnce(t, newReconciler(c, depsOK), byo); res.RequeueAfter != grantLagRetry {
		t.Errorf("bring your own: requeue = %v, want %v", res.RequeueAfter, grantLagRetry)
	}

	managed := managedForgeCR()
	managed.Spec.Forge.CredentialsSecret = "custom-forge"
	srv, objs := fakeForgejo(t, managed)
	c = testBuilder(t).WithObjects(append(objs, managed, binding(managed))...).WithInterceptorFuncs(refuseSecret("custom-forge", &refused)).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }
	if res := reconcileOnce(t, r, managed); res.RequeueAfter == 0 || res.RequeueAfter > grantLagRetry {
		t.Errorf("managed: requeue = %v, want within %v", res.RequeueAfter, grantLagRetry)
	}
}

// A grant that stays refused past the lag window is wrong (a verb, the
// Secret's name or the account): the pass falls back to the drift cadence, and
// the condition says what to replace and how, rather than repeating a create
// that would fail on the objects already there.
func TestWrongGrantRechecksBrieflyThenSaysReplace(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	now := time.Now().Truncate(time.Second)
	role, binding := install.ForgeCredentialGrant(dv, "dotvirt-operator")
	role.Rules[0].ResourceNames = []string{"another-secret"}
	role.CreationTimestamp, binding.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute)), metav1.NewTime(now.Add(-time.Minute))
	refused := true
	c := testBuilder(t).WithObjects(dv, creds, role, binding).WithInterceptorFuncs(refuseSecret(creds.Name, &refused)).Build()
	r := newReconciler(c, depsOK)
	r.OperatorNamespace = "dotvirt-operator"
	r.now = func() time.Time { return now }

	if res := reconcileOnce(t, r, dv); res.RequeueAfter != grantLagRetry {
		t.Errorf("a minute after the grant changed: requeue = %v, want %v", res.RequeueAfter, grantLagRetry)
	}
	now = now.Add(grantLagWindow + time.Minute)
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != driftResync {
		t.Errorf("past the window: requeue = %v, want %v", res.RequeueAfter, driftResync)
	}
	ready := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeCredentialReady)
	if ready == nil || ready.Reason != "GrantIncomplete" || strings.Contains(ready.Message, "rechecked") {
		t.Fatalf("ForgeCredentialReady = %+v, want GrantIncomplete without the recheck", ready)
	}
	credentialMessageNamesNothing(t, ready.Message,
		"Role and RoleBinding "+install.ForgeCredentialGrantName+" in the install namespace exist but do not grant the operator's account get", "replaces them")
}

// credentialMessageNamesNothing checks a ForgeCredentialReady message says
// each of want, and carries no command and no value an admin could copy: the
// operator writes it, so an admin derives each value from the cluster, as the
// README's commands show.
func credentialMessageNamesNothing(t *testing.T, msg string, want ...string) {
	t.Helper()
	for _, w := range append(want, "from the console (Install)", `"Forge credential grant"`) {
		if !strings.Contains(msg, w) {
			t.Errorf("ForgeCredentialReady message lacks %q:\n%s", w, msg)
		}
	}
	for _, leak := range []string{"kubectl", "byo-creds", "custom-forge", "dotvirt-operator:", "--verb", "--serviceaccount"} {
		if strings.Contains(msg, leak) {
			t.Errorf("ForgeCredentialReady message carries %q:\n%s", leak, msg)
		}
	}
}

// A managed forge never mints a token it may not store in an admin-named
// credential: it asks first (create and patch while the Secret is absent: a
// server-side apply needs both), and mints only once every answer is yes.
func TestManagedForgeMintsOnlyWhatItMayStore(t *testing.T) {
	for _, c := range []struct {
		name    string
		allow   func(authorizationv1.ResourceAttributes) bool
		allowed bool
	}{
		{"nothing granted", func(authorizationv1.ResourceAttributes) bool { return false }, false},
		{"create without patch", func(a authorizationv1.ResourceAttributes) bool { return a.Verb != "patch" }, false},
		{"create and patch", func(authorizationv1.ResourceAttributes) bool { return true }, true},
	} {
		allowed := c.allowed
		dv := managedForgeCR()
		dv.Spec.Forge.CredentialsSecret = "custom-forge"
		srv, objs, mints := countingForgejo(t, dv)
		var asked []authorizationv1.ResourceAttributes
		c := testBuilder(t).WithObjects(append(objs, dv)...).WithInterceptorFuncs(interceptor.Funcs{Create: answerReviewsBy(c.allow, &asked)}).Build()
		r := newReconciler(c, depsOK)
		r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }
		for range 3 {
			if _, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv)); err != nil {
				t.Fatal(err)
			}
		}
		want := int64(0)
		if allowed {
			want = 1
		}
		if got := mints.Load(); got != want {
			t.Errorf("allowed=%v: %d tokens minted over three passes, want %d", allowed, got, want)
		}
		ask := func(verb string) authorizationv1.ResourceAttributes {
			return authorizationv1.ResourceAttributes{Namespace: dv.Namespace, Verb: verb, Resource: "secrets", Name: "custom-forge"}
		}
		if len(asked) == 0 || asked[0] != ask("create") || (allowed && (len(asked) < 2 || asked[1] != ask("patch"))) {
			t.Errorf("allowed=%v: asked %+v, want create then patch on the absent credential", allowed, asked)
		}
		ready := cond(dv, dotvirtv1alpha1.ConditionForgeCredentialReady)
		if allowed != (ready != nil && ready.Status == metav1.ConditionTrue) {
			t.Errorf("allowed=%v: ForgeCredentialReady = %+v", allowed, ready)
		}
	}
}

// A managed forge whose grant reads but does not write, past the lag window:
// the forge-dependent phases wait without reading the credential, nothing is
// minted, and the pass keeps the drift cadence (no settle retry from them).
func TestManagedGetOnlyGrantSettlesAtTheDriftCadence(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.CredentialsSecret = "custom-forge"
	// An Argo URL, so the Argo webhook would register if it could; sync
	// impersonation off, whose own recheck is not what this is about.
	dv.Spec.ArgoCD.ServerURL = "https://argocd.example"
	dv.Spec.ArgoCD.SyncImpersonation.Enabled = new(false)
	srv, objs, mints := countingForgejo(t, dv)
	now := time.Now().Truncate(time.Second)
	role, binding := install.ForgeCredentialGrant(dv, "dotvirt-operator")
	role.Rules[0].Verbs = []string{"get"}
	role.CreationTimestamp, binding.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute)), metav1.NewTime(now.Add(-time.Minute))
	var asked []authorizationv1.ResourceAttributes
	c := testBuilder(t).WithObjects(append(objs, dv, role, binding)...).WithInterceptorFuncs(interceptor.Funcs{
		Create: answerReviewsBy(func(a authorizationv1.ResourceAttributes) bool { return a.Verb == "get" }, &asked),
	}).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }
	r.now = func() time.Time { return now }

	reconcileOnce(t, r, dv)
	markOperandReady(t, c, dv)
	now = now.Add(driftResync + grantLagWindow)
	for range 2 {
		if res := reconcileOnce(t, r, dv); res.RequeueAfter != driftResync {
			t.Errorf("past the window: requeue = %v, want %v", res.RequeueAfter, driftResync)
		}
	}
	if n := mints.Load(); n != 0 {
		t.Errorf("%d tokens minted it could not store", n)
	}
	got := getCR(t, c, dv)
	for _, ct := range []string{dotvirtv1alpha1.ConditionForgeRepoReady, dotvirtv1alpha1.ConditionArgoWebhook, dotvirtv1alpha1.ConditionDotvirtWebhook} {
		if c := cond(got, ct); c == nil || c.Status != metav1.ConditionUnknown || c.Reason != reasonCredentialWait {
			t.Errorf("%s = %+v, want Unknown/%s", ct, c, reasonCredentialWait)
		}
	}
}
