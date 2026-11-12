package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// appsetRefreshAnnotation makes Argo CD's ApplicationSet controller run the
// generators at once. It removes the annotation only after a run that
// succeeds, so one still present after a run means the run failed again.
const appsetRefreshAnnotation = "argocd.argoproj.io/application-set-refresh"

// generatorFailed is the ErrorOccurred reason Argo CD records when a generator
// returns an error: for dotvirt's plugin generator, the call to the dotvirt pod.
const generatorFailed = "ApplicationGenerationFromParamsError"

// refreshGrace is how long a refresh asked of Argo may go unanswered before
// the error it was asked about counts as current.
const refreshGrace = time.Minute

// refreshRetry is when a pass that asked for a refresh comes back to read
// Argo's answer, instead of the minute settleRetry waits.
const refreshRetry = 15 * time.Second

const (
	reasonWaitingForDotvirt = "WaitingForDotvirt"
	reasonRefreshing        = "Refreshing"
)

// appsetError is the ApplicationSet's ErrorOccurred condition while True.
type appsetError struct {
	msg, reason string
	since       time.Time
}

func applicationSetError(appset *unstructured.Unstructured) (appsetError, bool) {
	conds, _, _ := unstructured.NestedSlice(appset.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != "ErrorOccurred" || m["status"] != "True" {
			continue
		}
		var e appsetError
		e.reason, _ = m["reason"].(string)
		msg, _ := m["message"].(string)
		e.msg = fmt.Sprintf("ApplicationSet %s: %s", appset.GetName(), msg)
		if s, ok := m["lastTransitionTime"].(string); ok {
			e.since, _ = time.Parse(time.RFC3339, s)
		}
		return e, true
	}
	return appsetError{}, false
}

// appsetState is what ArgoReady says of the ApplicationSet.
type appsetState struct {
	degraded string
	// waiting is the reason ArgoReady is Unknown, with msg.
	waiting, msg string
	refresh      bool
}

// judgeAppSet reads the ApplicationSet's error against the dotvirt pod's
// readiness. The plugin generator calls the pod, which a rollout restarts
// (Recreate: the drafts volume is RWO), and Argo retries a failed generator
// only three minutes later. A generator failure the pod's downtime explains
// is not Degraded: while the pod is down it waits, and once the pod is back
// Argo is asked to run again now. Any other error, or the same one after
// Argo ran again, is Degraded.
func judgeAppSet(appset *unstructured.Unstructured, workload *metav1.Condition, now time.Time) appsetState {
	e, ok := applicationSetError(appset)
	if !ok {
		return appsetState{}
	}
	if e.reason != generatorFailed {
		return appsetState{degraded: e.msg}
	}
	if workload == nil || workload.Status != metav1.ConditionTrue {
		return appsetState{waiting: reasonWaitingForDotvirt, msg: "the ApplicationSet's generator waits for the dotvirt pod"}
	}
	// Conditions keep whole seconds once stored, as does the asked stamp.
	ready := workload.LastTransitionTime.Truncate(time.Second)
	if e.since.IsZero() || !e.since.Before(ready) {
		return appsetState{degraded: e.msg}
	}
	asked, err := time.Parse(time.RFC3339, appset.GetAnnotations()[appsetRefreshAnnotation])
	if err != nil || asked.Before(ready) {
		return appsetState{waiting: reasonRefreshing, msg: "the ApplicationSet runs its generator again now that the dotvirt pod is back", refresh: true}
	}
	if now.Sub(asked) < refreshGrace {
		return appsetState{waiting: reasonRefreshing, msg: "the ApplicationSet runs its generator again now that the dotvirt pod is back"}
	}
	return appsetState{degraded: e.msg}
}

// refreshAppSet stamps the refresh annotation with the time asked, which
// judgeAppSet reads back. A merge patch under its own field manager, outside
// the apply: the next apply then leaves the annotation for Argo to remove.
func (r *DotvirtReconciler) refreshAppSet(ctx context.Context, appset *unstructured.Unstructured) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, appsetRefreshAnnotation, r.clock().UTC().Format(time.RFC3339))
	return r.Patch(ctx, appset, client.RawPatch(types.MergePatchType, []byte(patch)), client.FieldOwner("dotvirt-operator-refresh"))
}
