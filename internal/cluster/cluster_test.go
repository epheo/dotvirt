package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// vmReadIn answers each list-virtualmachines SSAR by namespace: allowed in
// the namespaces of allow, a review failure in those of fail, denied elsewhere
// (the cluster-wide review included).
func vmReadIn(kube *fake.Clientset, allow, fail []string) {
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ns := ssar.Spec.ResourceAttributes.Namespace
		if slices.Contains(fail, ns) {
			return true, nil, apierrors.NewServiceUnavailable("review backend down")
		}
		ssar.Status.Allowed = ns != "" && slices.Contains(allow, ns)
		return true, ssar, nil
	})
}

// CanIn keeps only the namespaces whose review allows.
func TestCanIn(t *testing.T) {
	kube := fake.NewSimpleClientset()
	vmReadIn(kube, []string{"tenant-a", "tenant-c"}, nil)

	c := &Client{kube: kube}
	got, err := c.CanIn(context.Background(), "list", "kubevirt.io", "virtualmachines", []string{"tenant-c", "tenant-a", "tenant-b"})
	if err != nil {
		t.Fatalf("CanIn: %v", err)
	}
	if want := []string{"tenant-a", "tenant-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (only namespaces granting VM read should survive)", got, want)
	}
}

// TestCanInFailureIsAnError pins fail-closed without a false answer: one
// failed review makes the whole set unknown. Returning the others would read
// as lost access to the failed namespace and get cached as such.
func TestCanInFailureIsAnError(t *testing.T) {
	kube := fake.NewSimpleClientset()
	vmReadIn(kube, []string{"tenant-a", "tenant-b"}, []string{"tenant-b"})

	c := &Client{kube: kube}
	got, err := c.CanIn(context.Background(), "list", "kubevirt.io", "virtualmachines", []string{"tenant-a", "tenant-b"})
	if err == nil {
		t.Fatalf("got %v with nil error; a failed review must fail the probe", got)
	}
	if !apierrors.IsServiceUnavailable(err) {
		t.Errorf("err = %v; the review's own status must stay classifiable", err)
	}
}

// TestCanInCanceled pins the cancel path: a done context is the answer,
// never the reviews that happened to finish first.
func TestCanInCanceled(t *testing.T) {
	kube := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		cancel()
		ssar.Status.Allowed = true
		return true, ssar, nil
	})

	c := &Client{kube: kube}
	got, err := c.CanIn(ctx, "list", "kubevirt.io", "virtualmachines", []string{"tenant-a", "tenant-b", "tenant-c"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v; want context.Canceled", got, err)
	}
}

// TestCanInBoundsConcurrency pins the probe fan-out: parallel, so a
// many-namespace tenant is not a serial chain of reviews, and bounded. A real
// client over HTTP, since the fake clientset serializes every action.
func TestCanInBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
	}))
	defer srv.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, QPS: -1})
	if err != nil {
		t.Fatal(err)
	}
	candidates := make([]string, 40)
	for i := range candidates {
		candidates[i] = fmt.Sprintf("tenant-%02d", i)
	}

	c := &Client{kube: kube}
	got, err := c.CanIn(context.Background(), "list", "kubevirt.io", "virtualmachines", candidates)
	if err != nil || len(got) != len(candidates) {
		t.Fatalf("got %d namespaces, err %v; want all %d", len(got), err, len(candidates))
	}
	if p := peak.Load(); p < 2 || p > probeConcurrency {
		t.Errorf("peak concurrent reviews = %d, want 2..%d", p, probeConcurrency)
	}
}

// TestSetNodeMaintenanceRoundTrip drives enter/exit through the fake's real
// strategic-merge patching: one patch must flip the annotation and cordon
// together, and exit must remove the annotation (null value), not blank it.
func TestSetNodeMaintenanceRoundTrip(t *testing.T) {
	kube := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
	)
	c := &Client{kube: kube}
	ctx := context.Background()

	if err := c.SetNodeMaintenance(ctx, "w1", true); err != nil {
		t.Fatalf("enter: %v", err)
	}
	n, _ := kube.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{})
	if !n.Spec.Unschedulable {
		t.Error("enter should cordon the node")
	}
	if n.Annotations[maintenanceAnnotation] == "" {
		t.Error("enter should set the maintenance annotation")
	}

	if err := c.SetNodeMaintenance(ctx, "w1", false); err != nil {
		t.Fatalf("exit: %v", err)
	}
	n, _ = kube.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{})
	if n.Spec.Unschedulable {
		t.Error("exit should uncordon the node")
	}
	if _, ok := n.Annotations[maintenanceAnnotation]; ok {
		t.Error("exit should remove the maintenance annotation")
	}
}

// TestNodeInfoMaintenanceSurvivesUncordon: the annotation is the intent marker,
// so an out-of-band uncordon must not silently end maintenance mode.
func TestNodeInfoMaintenanceSurvivesUncordon(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        "w1",
		Annotations: map[string]string{maintenanceAnnotation: "true"},
	}})
	c := &Client{kube: kube}

	info, err := c.NodeInfo(context.Background(), "w1")
	if err != nil {
		t.Fatalf("NodeInfo: %v", err)
	}
	if !info.Maintenance {
		t.Error("maintenance should report from the annotation, not cordon state")
	}
	if info.Unschedulable {
		t.Error("unschedulable should reflect the node spec")
	}
}

func TestListNodesMaintenance(t *testing.T) {
	sched := map[string]string{"kubevirt.io/schedulable": "true"}
	kube := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1", Labels: sched}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:        "w2",
			Labels:      sched,
			Annotations: map[string]string{maintenanceAnnotation: "true"},
		}},
	)
	c := &Client{kube: kube}

	nodes, err := c.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Maintenance || !nodes[1].Maintenance {
		t.Errorf("want w1 not in maintenance, w2 in maintenance; got %+v", nodes)
	}
}

// LiveNamespace keeps what the namespace's author declared and drops what the
// cluster stamps on every namespace.
func TestLiveNamespaceKeepsAuthorMetadata(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "tenant-a",
		Labels: map[string]string{
			"tenant": "a", "k8s.ovn.org/primary-user-defined-network": "",
			"pod-security.kubernetes.io/enforce":      "restricted",
			"kubernetes.io/metadata.name":             "tenant-a",
			"pod-security.kubernetes.io/audit":        "restricted",
			"pod-security.kubernetes.io/warn-version": "latest",
		},
		Annotations: map[string]string{
			"openshift.io/description":                                     "roundtable",
			"kubectl.kubernetes.io/last-applied-configuration":             "{}",
			"openshift.io/sa.scc.uid-range":                                "1000/10000",
			"security.openshift.io/MinimallySufficientPodSecurityStandard": "restricted",
		},
	}})
	c := &Client{kube: kube}
	got, err := c.LiveNamespace(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("LiveNamespace: %v", err)
	}
	wantLabels := map[string]string{
		"tenant": "a", "k8s.ovn.org/primary-user-defined-network": "", "pod-security.kubernetes.io/enforce": "restricted",
	}
	if !reflect.DeepEqual(got.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", got.Labels, wantLabels)
	}
	if want := map[string]string{"openshift.io/description": "roundtable"}; !reflect.DeepEqual(got.Annotations, want) {
		t.Errorf("annotations = %v, want %v", got.Annotations, want)
	}
	if absent, err := c.LiveNamespace(context.Background(), "new"); err != nil || absent != nil {
		t.Errorf("a namespace that does not exist must read as nil, got %v, %v", absent, err)
	}
}

// The node LIST uplinks already read carries each node's OVN-Kubernetes pod
// subnets, the default network's range the trace places addresses in.
func TestListNodeLabelsCarriesPodSubnets(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "w1", Labels: map[string]string{"zone": "a"},
		Annotations: map[string]string{"k8s.ovn.org/node-subnets": `{"default":["10.128.0.0/23"]}`},
	}})
	c := &Client{kube: kube}
	got, err := c.ListNodeLabels(context.Background())
	if err != nil || len(got) != 1 || got[0].Labels["zone"] != "a" || got[0].PodSubnets != `{"default":["10.128.0.0/23"]}` {
		t.Fatalf("ListNodeLabels = %+v, %v", got, err)
	}
}
