package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// An admin-named credential the operator may not read: the forge-dependent
// phases wait on the grant, the rest of the install runs, and nothing the
// ArgoCD tier rendered before is pruned. Once granted, the pass goes on.
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

	// A grant can stay pending for days: the wait comes back at the drift cadence.
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != driftResync {
		t.Errorf("requeue = %v, want %v", res.RequeueAfter, driftResync)
	}
	got := getCR(t, c, dv)
	granted := cond(got, dotvirtv1alpha1.ConditionForgeCredentialGranted)
	if granted == nil || granted.Status != metav1.ConditionFalse || granted.Reason != "NotGranted" ||
		!strings.Contains(granted.Message, install.ForgeCredentialGrantName) || !strings.Contains(granted.Message, "console") {
		t.Errorf("ForgeCredentialGranted = %+v, want False/NotGranted naming the grant and the console", granted)
	}
	for _, ct := range []string{
		dotvirtv1alpha1.ConditionArgoReady, dotvirtv1alpha1.ConditionForgeRepoReady,
		dotvirtv1alpha1.ConditionArgoWebhook, dotvirtv1alpha1.ConditionDotvirtWebhook,
	} {
		if c := cond(got, ct); c == nil || c.Status != metav1.ConditionUnknown || c.Reason != reasonCredentialNotGranted {
			t.Errorf("%s = %+v, want Unknown/%s", ct, c, reasonCredentialNotGranted)
		}
	}
	if c := cond(got, dotvirtv1alpha1.ConditionForgeReady); c == nil || c.Reason != reasonCredentialNotGranted {
		t.Errorf("ForgeReady = %+v, want it waiting on the grant", c)
	}
	// The workload phase ran to its end: ArgoReady, after it, is the one waiting.
	if c := cond(got, dotvirtv1alpha1.ConditionWorkloadReady); c == nil || c.Reason == reasonCredentialNotGranted {
		t.Errorf("WorkloadReady = %+v: the workload does not need the credential and must not wait", c)
	}
	if !exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("the operand was not applied")
	}
	if !exists(t, c, &corev1.Secret{}, "argocd", install.RepoCredsName) {
		t.Error("the waiting pass pruned the repo credentials Argo still clones with")
	}

	refused = false
	reconcileOnce(t, r, dv)
	got = getCR(t, c, dv)
	if granted := cond(got, dotvirtv1alpha1.ConditionForgeCredentialGranted); granted == nil || granted.Status != metav1.ConditionTrue {
		t.Errorf("granted, ForgeCredentialGranted = %+v, want True", granted)
	}
	if c := cond(got, dotvirtv1alpha1.ConditionArgoReady); c == nil || c.Reason == reasonCredentialNotGranted {
		t.Errorf("granted, ArgoReady = %+v, still waiting", c)
	}
}

// A managed forge writes its minted token into the credential: a refused write
// is the grant missing too, and the rest of the install goes on.
func TestManagedForgeWaitsOnACredentialItMayNotWrite(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.CredentialsSecret = "custom-forge"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/tokens/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sha1":"tok123"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	readyForgejo := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoServiceName, Namespace: dv.Namespace},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
	admin := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoAdminSecret, Namespace: dv.Namespace},
		Data:       map[string][]byte{"password": []byte("adminpw")},
	}
	refused := false
	funcs := refuseSecret("custom-forge", &refused)
	// The read is allowed (the Secret is simply absent); only the write is refused.
	funcs.Get = nil
	refused = true
	c := testBuilder(t).WithObjects(dv, readyForgejo, admin).WithInterceptorFuncs(funcs).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	res, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv))
	if err != nil || res != nil {
		t.Fatalf("reconcileForge = %v, %v; want the pass to go on", res, err)
	}
	granted := cond(dv, dotvirtv1alpha1.ConditionForgeCredentialGranted)
	if granted == nil || granted.Status != metav1.ConditionFalse || !strings.Contains(granted.Message, "read and write") {
		t.Errorf("ForgeCredentialGranted = %+v, want False asking for read and write", granted)
	}
	if c := cond(dv, dotvirtv1alpha1.ConditionForgeReady); c == nil || c.Reason != reasonCredentialNotGranted {
		t.Errorf("ForgeReady = %+v, want it waiting on the grant", c)
	}
}

// A credential the operator's own role names needs no grant: the condition is
// absent, and one left from an earlier custom name goes.
func TestOwnCredentialNeedsNoGrant(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge.CredentialsSecret = "dotvirt-forge"
	dv.Status.Conditions = []metav1.Condition{{
		Type: dotvirtv1alpha1.ConditionForgeCredentialGranted, Status: metav1.ConditionFalse, Reason: "NotGranted", LastTransitionTime: metav1.Now(),
	}}
	c := testBuilder(t).WithObjects(dv).WithStatusSubresource(dv).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeCredentialGranted); got != nil {
		t.Errorf("ForgeCredentialGranted = %+v for the operator's own credential", got)
	}
}
