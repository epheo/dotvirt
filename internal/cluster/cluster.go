// Package cluster is dotvirt's live-read plane: it lists VirtualMachines and
// their running VirtualMachineInstances from the cluster. Identity is per-token:
// a Factory mints one Client per bearer token, so cluster RBAC — not dotvirt — is
// the sole authority on what a caller may read. It never writes; Argo owns apply.
package cluster

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// Client reads VM and namespace state from the cluster under one identity (one
// bearer token). Built by a Factory; never constructed directly.
type Client struct {
	kubevirt kubecli.KubevirtClient
	kube     kubernetes.Interface
	dyn      dynamic.Interface // for CRDs without a typed client (instancetypes, etc.)
}

// Factory mints per-token Clients. The shared restfactory owns the identity
// machinery (credential-less base config, SA-token capture, per-token caching);
// this wrapper only knows how to assemble a *Client and expose the SA kube client.
type Factory struct {
	*restfactory.Factory[*Client]
}

// NewFactory builds a Factory. kubeconfig empty means in-cluster config.
func NewFactory(kubeconfig string) (*Factory, error) {
	base, err := restfactory.New(kubeconfig, clientsFor)
	if err != nil {
		return nil, err
	}
	return &Factory{base}, nil
}

// SAKube exposes the SA-identity typed kube client, which the auth package needs
// to call TokenReview (a cluster-level operation done as dotvirt, not the user).
func (f *Factory) SAKube() (kubernetes.Interface, error) {
	c, err := f.SA()
	if err != nil {
		return nil, err
	}
	return c.kube, nil
}

// NewClient assembles a Client from already-built typed clients. The Factory uses
// it after constructing per-token clients; tests use it with fakes.
func NewClient(kube kubernetes.Interface, kubevirt kubecli.KubevirtClient, dyn dynamic.Interface) *Client {
	return &Client{kubevirt: kubevirt, kube: kube, dyn: dyn}
}

// clientsFor is the restfactory build hook: a token-bearing config → a full Client.
func clientsFor(cfg *rest.Config) (*Client, error) {
	kv, err := kubecli.GetKubevirtClientFromRESTConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubevirt client: %w", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return NewClient(kube, kv, dyn), nil
}

// VisibleNamespaces returns the namespaces this client's token may read VMs in.
// The fast path is a cluster-wide Namespaces().List. A token without that
// cluster-level permission gets Forbidden; we then fall back to probing each
// candidate namespace with a SelfSubjectRulesReview and keeping those that grant
// get/list on virtualmachines or pods. candidates is the SA-discovered project
// namespace set; without it the fallback has nothing to probe and returns empty.
func (c *Client) VisibleNamespaces(ctx context.Context, candidates []string) ([]string, error) {
	nsList, err := c.kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err == nil {
		names := make([]string, 0, len(nsList.Items))
		for i := range nsList.Items {
			names = append(names, nsList.Items[i].Name)
		}
		return names, nil
	}
	if !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	// Forbidden to list namespaces cluster-wide: probe the candidate set instead.
	// Skip (don't abort) on a per-candidate probe error so one transient/odd
	// namespace can't blank the whole inventory.
	var out []string
	for _, ns := range candidates {
		ok, err := c.canReadVMs(ctx, ns)
		if err != nil {
			log.Printf("visible namespaces: probe %s: %v (skipping)", ns, err)
			continue
		}
		if ok {
			out = append(out, ns)
		}
	}
	return out, nil
}

// VMListWatch is the List+Watch source for a cluster-wide VirtualMachine
// reflector (run on the SA client). It backs clusterstate's snapshot so the read
// path never lists VMs itself.
func (c *Client) VMListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.kubevirt.VirtualMachine(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.kubevirt.VirtualMachine(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// VMIListWatch is the List+Watch source for a cluster-wide
// VirtualMachineInstance reflector — the running-state half of the snapshot.
func (c *Client) VMIListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.kubevirt.VirtualMachineInstance(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.kubevirt.VirtualMachineInstance(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// NamespaceListWatch is the List+Watch source for a project-namespace reflector,
// restricted to namespaces carrying label (dotvirt's project label). It feeds the
// topology half of the snapshot so the read path never GETs namespaces per build.
func (c *Client) NamespaceListWatch(label string) *cache.ListWatch {
	withLabel := func(o metav1.ListOptions) metav1.ListOptions {
		o.LabelSelector = label
		return o
	}
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.kube.CoreV1().Namespaces().List(ctx, withLabel(o))
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.kube.CoreV1().Namespaces().Watch(ctx, withLabel(o))
		},
	}
}

// RoleBindingListWatch is the List+Watch source for a cluster-wide RoleBinding
// reflector (run on the SA client). dotvirt never reads RoleBindings; it watches
// them only to learn when a token's namespace visibility may have changed, so the
// per-token visible-namespace cache can be invalidated promptly instead of on a
// blind TTL.
func (c *Client) RoleBindingListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.kube.RbacV1().RoleBindings(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.kube.RbacV1().RoleBindings(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// canReadVMs reports whether this token may get/list VirtualMachines in ns, via a
// SelfSubjectRulesReview. It checks VM read specifically (not a broader proxy like
// pods): a namespace's project membership exposes its git repo URL, so only users
// who can actually read its VMs should learn it.
func (c *Client) canReadVMs(ctx context.Context, ns string) (bool, error) {
	review := &authzv1.SelfSubjectRulesReview{Spec: authzv1.SelfSubjectRulesReviewSpec{Namespace: ns}}
	res, err := c.kube.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("rules review for %s: %w", ns, err)
	}
	for _, rule := range res.Status.ResourceRules {
		if grantsRead(rule, "kubevirt.io", "virtualmachines") {
			return true, nil
		}
	}
	return false, nil
}

// allowed reports whether this token may perform the action described by attrs, via
// a SelfSubjectAccessReview — the single SSAR primitive the capability checks share.
func (c *Client) allowed(ctx context.Context, attrs *authzv1.ResourceAttributes) (bool, error) {
	review := &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs}}
	res, err := c.kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

// CanUpdateVM reports whether this token may update the VirtualMachine ns/name. It
// gates the re-sync action: a user may drive an Argo reconcile of a VM (run with
// dotvirt's SA) only if they could modify that VM themselves — so read-only callers
// can't escalate into an SA-privileged sync.
func (c *Client) CanUpdateVM(ctx context.Context, namespace, name string) (bool, error) {
	ok, err := c.allowed(ctx, &authzv1.ResourceAttributes{
		Namespace: namespace, Verb: "update", Group: "kubevirt.io", Resource: "virtualmachines", Name: name,
	})
	if err != nil {
		return false, fmt.Errorf("access review for %s/%s: %w", namespace, name, err)
	}
	return ok, nil
}

// grantsRead reports whether a resource rule allows get or list on the given
// group/resource. "*" matches any verb, group, or resource.
func grantsRead(rule authzv1.ResourceRule, group, resource string) bool {
	return contains(rule.Verbs, "get", "list") &&
		contains(rule.APIGroups, group) &&
		contains(rule.Resources, resource)
}

// contains reports whether haystack holds "*" or any of the wanted values.
func contains(haystack []string, wanted ...string) bool {
	for _, h := range haystack {
		if h == "*" {
			return true
		}
		for _, w := range wanted {
			if h == w {
				return true
			}
		}
	}
	return false
}

// VNCConn opens a VNC stream to a running VMI and returns it as a net.Conn
// carrying the RFB protocol. The caller bridges it to the browser's noVNC
// WebSocket. preserveSession=false so each connection is independent.
func (c *Client) VNCConn(namespace, name string) (net.Conn, error) {
	stream, err := c.kubevirt.VirtualMachineInstance(namespace).VNC(name, false)
	if err != nil {
		return nil, fmt.Errorf("open VNC for %s/%s: %w", namespace, name, err)
	}
	return stream.AsConn(), nil
}

// Screenshot returns a PNG of the VMI's graphical console (the vnc/screenshot
// subresource), for the Summary's console preview. Needs a running VMI with a
// VNC-capable graphics device; errors otherwise (the UI just hides the thumb).
func (c *Client) Screenshot(ctx context.Context, namespace, name string) ([]byte, error) {
	return c.kubevirt.VirtualMachineInstance(namespace).Screenshot(ctx, name, &kubevirtcorev1.ScreenshotOptions{})
}

// ListEvents returns recent Kubernetes Events for the VM ns/name and its VMI
// (which shares the name), newest-first — the per-VM Monitor tab. Read with this
// client's token, so cluster RBAC gates it like every other read.
func (c *Client) ListEvents(ctx context.Context, namespace, name string) ([]model.Event, error) {
	list, err := c.kube.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name,
	})
	if err != nil {
		return nil, fmt.Errorf("list events for %s/%s: %w", namespace, name, err)
	}
	out := make([]model.Event, 0, len(list.Items))
	for i := range list.Items {
		if isVMEvent(&list.Items[i]) {
			out = append(out, eventOf(&list.Items[i]))
		}
	}
	sortEventsDesc(out)
	return out, nil
}

// ListVMEvents returns recent VM/VMI Events across the given namespaces (the set
// the caller may see), newest-first and capped — the dock's Events lane. Listed
// per-namespace with this client's token, so cluster RBAC gates it and nothing
// leaks across tenants. Field selectors can't OR, so it's one selected LIST per
// kind — still far cheaper than listing every event in a busy namespace (pod
// churn dominates) and filtering here.
func (c *Client) ListVMEvents(ctx context.Context, namespaces []string) ([]model.Event, error) {
	out := []model.Event{}
	for _, ns := range namespaces {
		for _, kind := range []string{"VirtualMachine", "VirtualMachineInstance"} {
			list, err := c.kube.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
				FieldSelector: "involvedObject.kind=" + kind,
			})
			if err != nil {
				return nil, fmt.Errorf("list events in %s: %w", ns, err)
			}
			for i := range list.Items {
				out = append(out, eventOf(&list.Items[i]))
			}
		}
	}
	sortEventsDesc(out)
	if len(out) > 200 {
		out = out[:200] // the dock shows the most recent; cap to bound the payload
	}
	return out, nil
}

// isVMEvent reports whether an Event is about a VirtualMachine or its VMI (events
// share the VM's name with the virt-launcher Pod, hence the kind check).
func isVMEvent(e *corev1.Event) bool {
	k := e.InvolvedObject.Kind
	return k == "VirtualMachine" || k == "VirtualMachineInstance"
}

// eventOf maps a Kubernetes Event to the model DTO, taking the most recent of the
// legacy LastTimestamp, the new-style EventTime, or the creation time.
func eventOf(e *corev1.Event) model.Event {
	ts := e.LastTimestamp.Time
	if ts.IsZero() {
		ts = e.EventTime.Time
	}
	if ts.IsZero() {
		ts = e.CreationTimestamp.Time
	}
	last := ""
	if !ts.IsZero() {
		last = ts.UTC().Format(time.RFC3339)
	}
	ns := e.InvolvedObject.Namespace
	if ns == "" {
		ns = e.Namespace
	}
	return model.Event{
		Namespace: ns,
		Name:      e.InvolvedObject.Name,
		Type:      e.Type,
		Reason:    e.Reason,
		Message:   e.Message,
		Count:     e.Count,
		Object:    e.InvolvedObject.Kind,
		LastSeen:  last,
	}
}

func sortEventsDesc(events []model.Event) {
	// Newest first; RFC3339 sorts lexically, undated events sink to the end.
	sort.Slice(events, func(i, j int) bool { return events[i].LastSeen > events[j].LastSeen })
}

// --- runtime ops (imperative, RBAC-gated) ---
//
// These run under the caller's token and act on the live VMI without touching
// the git-managed VM spec, so ArgoCD self-heal leaves them alone — unlike power,
// which is a spec edit and stays in the PR lane.

// Restart restarts the VM: the running VMI is recreated per its run strategy.
func (c *Client) Restart(ctx context.Context, namespace, name string) error {
	return c.kubevirt.VirtualMachine(namespace).Restart(ctx, name, &kubevirtcorev1.RestartOptions{})
}

// Migrate live-migrates the running VMI to another node (the vMotion analog).
// A non-empty targetNode pins the destination via the migration's added node
// selector (kubernetes.io/hostname); empty leaves the choice to the scheduler.
// The selector can only narrow the VM's own constraints, never bypass them.
func (c *Client) Migrate(ctx context.Context, namespace, name, targetNode string) error {
	opts := &kubevirtcorev1.MigrateOptions{}
	if targetNode != "" {
		opts.AddedNodeSelector = map[string]string{corev1.LabelHostname: targetNode}
	}
	return c.kubevirt.VirtualMachine(namespace).Migrate(ctx, name, opts)
}

// Pause freezes the running VMI (vCPUs stopped, memory retained).
func (c *Client) Pause(ctx context.Context, namespace, name string) error {
	return c.kubevirt.VirtualMachineInstance(namespace).Pause(ctx, name, &kubevirtcorev1.PauseOptions{})
}

// Unpause resumes a paused VMI.
func (c *Client) Unpause(ctx context.Context, namespace, name string) error {
	return c.kubevirt.VirtualMachineInstance(namespace).Unpause(ctx, name, &kubevirtcorev1.UnpauseOptions{})
}

// --- node maintenance (cordon/uncordon + maintenance mode; the By-Node view) ---

// maintenanceAnnotation marks a node the user put in maintenance mode via
// dotvirt. Cordon alone can't carry that intent: a merely-cordoned node is not
// in maintenance, and maintenance must survive an out-of-band uncordon until
// the user explicitly exits it.
const maintenanceAnnotation = "dotvirt.io/maintenance"

// NodeInfo reads a node's schedulability under the caller's token, plus whether
// that token may cordon it (an SSAR on node update) so the UI gates the action.
// A read failure (no node-get RBAC) surfaces as an error the handler maps to
// 403/404 — the action stays hidden for users who can't see nodes.
func (c *Client) NodeInfo(ctx context.Context, name string) (model.NodeInfo, error) {
	node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return model.NodeInfo{}, err
	}
	can, _ := c.canPatchNodes(ctx) // best-effort; false on review error
	return model.NodeInfo{
		Name:          name,
		Unschedulable: node.Spec.Unschedulable,
		Maintenance:   node.Annotations[maintenanceAnnotation] != "",
		CanCordon:     can,
	}, nil
}

// SetNodeCordon patches node.spec.unschedulable under the caller's token, so
// cluster RBAC is the sole gate (a user without node-update gets 403). Cordon
// stops new placements; running VMIs stay until an evacuation migrates them.
func (c *Client) SetNodeCordon(ctx context.Context, name string, unschedulable bool) error {
	patch := fmt.Appendf(nil, `{"spec":{"unschedulable":%t}}`, unschedulable)
	_, err := c.kube.CoreV1().Nodes().Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// SetNodeMaintenance enters or exits maintenance mode: one patch flips the
// annotation and spec.unschedulable together so the intent marker and its
// cordon enforcement can't diverge. Same caller-token gate as cordon; the
// evacuation itself stays with the per-VM Migrate so each move carries its
// own RBAC check.
func (c *Client) SetNodeMaintenance(ctx context.Context, name string, enter bool) error {
	var patch []byte
	if enter {
		patch = fmt.Appendf(nil, `{"metadata":{"annotations":{%q:"true"}},"spec":{"unschedulable":true}}`, maintenanceAnnotation)
	} else {
		patch = fmt.Appendf(nil, `{"metadata":{"annotations":{%q:null}},"spec":{"unschedulable":false}}`, maintenanceAnnotation)
	}
	_, err := c.kube.CoreV1().Nodes().Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// ListNodes returns the cluster's virtualization hosts — nodes KubeVirt marks
// schedulable for VMs — under the caller's token, as candidate live-migration
// targets. No node-list RBAC surfaces as an error (→ 403) and the migrate
// dialog falls back to scheduler-picked placement. Ready + cordon state ride
// along so the picker can gray out hosts a migration could not land on.
func (c *Client) ListNodes(ctx context.Context) ([]model.Node, error) {
	list, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "kubevirt.io/schedulable=true"})
	if err != nil {
		return nil, err
	}
	out := make([]model.Node, 0, len(list.Items))
	for _, n := range list.Items {
		out = append(out, model.Node{
			Name:          n.Name,
			Ready:         nodeReady(n),
			Unschedulable: n.Spec.Unschedulable,
			Maintenance:   n.Annotations[maintenanceAnnotation] != "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func nodeReady(n corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// CanReadNodes reports whether this token may list nodes (cluster-scoped). It
// gates the networking read's physical fabric (Uplinks + Physical adapters):
// those are node-level infrastructure, so a plain tenant who can't see nodes
// doesn't see node NICs either. Best-effort — a review error reads as "no".
func (c *Client) CanReadNodes(ctx context.Context) bool {
	ok, _ := c.allowed(ctx, &authzv1.ResourceAttributes{Verb: "list", Resource: "nodes"})
	return ok
}

// canPatchNodes reports whether this token may update nodes (cluster-scoped).
func (c *Client) canPatchNodes(ctx context.Context) (bool, error) {
	return c.allowed(ctx, &authzv1.ResourceAttributes{Verb: "update", Resource: "nodes"})
}

// CanCreateClusterResource reports whether this token may create the cluster-scoped
// group/resource. It gates dotvirt's platform/Infrastructure authoring actions
// (CUDN, NNCP, Namespace): the user never creates these — Argo applies them from the
// platform repo — so can-i-create is the authorization SIGNAL standing in for "is a
// platform operator" (matching the dotvirt-platform-network-admin role). Best-effort
// like CanReadNodes — a review error reads as "no".
func (c *Client) CanCreateClusterResource(ctx context.Context, group, resource string) bool {
	ok, _ := c.allowed(ctx, &authzv1.ResourceAttributes{Verb: "create", Group: group, Resource: resource})
	return ok
}
