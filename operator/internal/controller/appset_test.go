package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

func generatorErrorAppSet(dv *dotvirtv1alpha1.Dotvirt, since time.Time, asked string) *unstructured.Unstructured {
	appset := argoObj("ApplicationSet", "argocd", "dotvirt-projects", install.Labels(dv))
	if asked != "" {
		appset.SetAnnotations(map[string]string{appsetRefreshAnnotation: asked})
	}
	appset.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{
			"type": "ErrorOccurred", "status": "True", "reason": generatorFailed,
			"message":            "error generating params from plugin: connection refused",
			"lastTransitionTime": since.UTC().Format(time.RFC3339),
		},
	}}
	return appset
}

func TestJudgeAppSet(t *testing.T) {
	ready := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	up := &metav1.Condition{Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(ready)}
	down := &metav1.Condition{Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(ready)}
	dv := testCR()
	stamp := func(at time.Time) string { return at.Format(time.RFC3339) }
	other := generatorErrorAppSet(dv, ready.Add(-time.Minute), "")
	other.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["reason"] = "RenderTemplateParamsError"
	noSince := generatorErrorAppSet(dv, ready, "")
	delete(noSince.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any), "lastTransitionTime")

	for _, c := range []struct {
		name     string
		appset   *unstructured.Unstructured
		workload *metav1.Condition
		now      time.Time
		want     appsetState
	}{
		{"no error", argoObj("ApplicationSet", "argocd", "dotvirt-projects", nil), up, ready, appsetState{}},
		{"a template error is not the pod's", other, up, ready, appsetState{degraded: "ApplicationSet dotvirt-projects: error generating params from plugin: connection refused"}},
		{"pod down", generatorErrorAppSet(dv, ready, ""), down, ready, appsetState{waiting: reasonWaitingForDotvirt}},
		{"no workload condition yet", generatorErrorAppSet(dv, ready, ""), nil, ready, appsetState{waiting: reasonWaitingForDotvirt}},
		{"failed after the pod was back", generatorErrorAppSet(dv, ready.Add(time.Second), ""), up, ready, appsetState{degraded: "x"}},
		{"failed in the same second the pod came back", generatorErrorAppSet(dv, ready, ""), up, ready, appsetState{degraded: "x"}},
		{"no transition time", noSince, up, ready, appsetState{degraded: "x"}},
		{"failed while the pod was down: refresh", generatorErrorAppSet(dv, ready.Add(-30*time.Second), ""), up, ready, appsetState{waiting: reasonRefreshing, refresh: true}},
		{"a refresh asked before this rollout: ask again", generatorErrorAppSet(dv, ready.Add(-30*time.Second), stamp(ready.Add(-time.Hour))), up, ready, appsetState{waiting: reasonRefreshing, refresh: true}},
		{"someone else's refresh: ask with a stamp", generatorErrorAppSet(dv, ready.Add(-30*time.Second), "true"), up, ready, appsetState{waiting: reasonRefreshing, refresh: true}},
		{"asked, Argo not answered yet", generatorErrorAppSet(dv, ready.Add(-30*time.Second), stamp(ready)), up, ready.Add(refreshGrace - time.Second), appsetState{waiting: reasonRefreshing}},
		{"asked, Argo ran again and failed", generatorErrorAppSet(dv, ready.Add(-30*time.Second), stamp(ready)), up, ready.Add(refreshGrace), appsetState{degraded: "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := judgeAppSet(c.appset, c.workload, c.now)
			if (got.degraded != "") != (c.want.degraded != "") || got.waiting != c.want.waiting || got.refresh != c.want.refresh {
				t.Errorf("judgeAppSet = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A generator failure from the dotvirt pod's restart: once the pod is back the
// pass asks Argo to run again and comes back soon, ArgoReady Unknown, not
// Degraded; Argo's next run clears it.
func TestRestartedPodRefreshesTheAppSet(t *testing.T) {
	dv := optOut(testCR())
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)
	markOperandReady(t, c, dv)
	reconcileOnce(t, r, dv)
	workload := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionWorkloadReady)
	if workload == nil || workload.Status != metav1.ConditionTrue {
		t.Fatalf("WorkloadReady = %+v, want True", workload)
	}

	errored := generatorErrorAppSet(dv, workload.LastTransitionTime.Add(-time.Minute), "")
	live := argoObj("ApplicationSet", "argocd", "dotvirt-projects", nil)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "argocd", Name: "dotvirt-projects"}, live); err != nil {
		t.Fatal(err)
	}
	live.Object["status"] = errored.Object["status"]
	if err := c.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}

	if res := reconcileOnce(t, r, dv); res.RequeueAfter != refreshRetry {
		t.Errorf("RequeueAfter = %v, want %v while Argo runs again", res.RequeueAfter, refreshRetry)
	}
	argo := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionArgoReady)
	if argo == nil || argo.Status != metav1.ConditionUnknown || argo.Reason != reasonRefreshing {
		t.Fatalf("ArgoReady = %+v, want Unknown/%s", argo, reasonRefreshing)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "argocd", Name: "dotvirt-projects"}, live); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, live.GetAnnotations()[appsetRefreshAnnotation]); err != nil {
		t.Fatalf("refresh annotation = %q, want a time stamp", live.GetAnnotations()[appsetRefreshAnnotation])
	}

	// The next apply leaves the annotation to Argo.
	reconcileOnce(t, r, dv)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "argocd", Name: "dotvirt-projects"}, live); err != nil {
		t.Fatal(err)
	}
	if _, ok := live.GetAnnotations()[appsetRefreshAnnotation]; !ok {
		t.Fatal("the apply removed the refresh annotation before Argo answered")
	}

	// Argo ran the generator: the error and the annotation are gone.
	delete(live.Object, "status")
	live.SetAnnotations(nil)
	if err := c.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, dv)
	if argo := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionArgoReady); !meta.IsStatusConditionTrue([]metav1.Condition{*argo}, dotvirtv1alpha1.ConditionArgoReady) {
		t.Fatalf("ArgoReady = %+v, want True", argo)
	}
}
