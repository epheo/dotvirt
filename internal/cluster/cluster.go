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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
		ListFunc: func(o metav1.ListOptions) (runtime.Object, error) {
			return c.kubevirt.VirtualMachine(metav1.NamespaceAll).List(context.Background(), o)
		},
		WatchFunc: func(o metav1.ListOptions) (watch.Interface, error) {
			return c.kubevirt.VirtualMachine(metav1.NamespaceAll).Watch(context.Background(), o)
		},
	}
}

// VMIListWatch is the List+Watch source for a cluster-wide
// VirtualMachineInstance reflector — the running-state half of the snapshot.
func (c *Client) VMIListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListFunc: func(o metav1.ListOptions) (runtime.Object, error) {
			return c.kubevirt.VirtualMachineInstance(metav1.NamespaceAll).List(context.Background(), o)
		},
		WatchFunc: func(o metav1.ListOptions) (watch.Interface, error) {
			return c.kubevirt.VirtualMachineInstance(metav1.NamespaceAll).Watch(context.Background(), o)
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
		ListFunc: func(o metav1.ListOptions) (runtime.Object, error) {
			return c.kube.CoreV1().Namespaces().List(context.Background(), withLabel(o))
		},
		WatchFunc: func(o metav1.ListOptions) (watch.Interface, error) {
			return c.kube.CoreV1().Namespaces().Watch(context.Background(), withLabel(o))
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

// CanUpdateVM reports whether this token may update the VirtualMachine ns/name,
// via a SelfSubjectAccessReview. It gates the re-sync action: a user may drive an
// Argo reconcile of a VM (run with dotvirt's SA) only if they could modify that VM
// themselves — so read-only callers can't escalate into an SA-privileged sync.
func (c *Client) CanUpdateVM(ctx context.Context, namespace, name string) (bool, error) {
	review := &authzv1.SelfSubjectAccessReview{
		Spec: authzv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      "update",
				Group:     "kubevirt.io",
				Resource:  "virtualmachines",
				Name:      name,
			},
		},
	}
	res, err := c.kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("access review for %s/%s: %w", namespace, name, err)
	}
	return res.Status.Allowed, nil
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

// ListVMObjects returns the VirtualMachine objects in the given namespaces, for
// export to the running branch. Callers strip server-set fields before
// serializing (see export.go).
func (c *Client) ListVMObjects(ctx context.Context, namespaces []string) ([]kubevirtcorev1.VirtualMachine, error) {
	var all []kubevirtcorev1.VirtualMachine
	for _, ns := range namespaces {
		vms, err := c.kubevirt.VirtualMachine(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list VMs in %s: %w", ns, err)
		}
		all = append(all, vms.Items...)
	}
	return all, nil
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
		e := &list.Items[i]
		kind := e.InvolvedObject.Kind
		if kind != "VirtualMachine" && kind != "VirtualMachineInstance" {
			continue // same name, unrelated object (e.g. the virt-launcher Pod) — skip
		}
		// Prefer the legacy LastTimestamp; new-style events only set EventTime.
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
		out = append(out, model.Event{
			Type: e.Type, Reason: e.Reason, Message: e.Message,
			Count: e.Count, Object: kind, LastSeen: last,
		})
	}
	// Newest first; RFC3339 sorts lexically, undated events sink to the end.
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out, nil
}
