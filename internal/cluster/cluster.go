// Package cluster is dotvirt's live-read plane: it lists VirtualMachines and
// their running VirtualMachineInstances from the cluster, scoped to namespaces
// matching a label selector. It never writes — Argo owns the apply path.
package cluster

import (
	"context"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"

	"github.com/epheo/dotvirt/internal/model"
)

// Client reads VM and namespace state from the cluster.
type Client struct {
	kubevirt kubecli.KubevirtClient
	kube     kubernetes.Interface
	dyn      dynamic.Interface // for CRDs without a typed client (instancetypes, etc.)
	nsLabel  string            // label selector for project namespaces; empty = all VM namespaces
}

// New builds a Client. kubeconfig empty means in-cluster config. nsLabel is a
// label selector (e.g. "dotvirt.io/project") restricting which namespaces are
// treated as projects; empty means every namespace that has VMs.
func New(kubeconfig, nsLabel string) (*Client, error) {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
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
	return &Client{kubevirt: kv, kube: kube, dyn: dyn, nsLabel: nsLabel}, nil
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// LiveVM is the actual state of one VM, keyed by namespace/name for merging into
// the git-derived inventory.
type LiveVM struct {
	Phase    string
	GuestIP  string
	NodeName string
	Ready    bool
}

// LiveState lists VMs in the scoped namespaces and returns their actual state
// keyed by "namespace/name". VMIs supply phase/IP/node; a VM without a VMI is
// reported with an empty (stopped) state.
func (c *Client) LiveState(ctx context.Context) (map[string]LiveVM, error) {
	namespaces, err := c.projectNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]LiveVM{}
	for _, ns := range namespaces {
		vms, err := c.kubevirt.VirtualMachine(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list VMs in %s: %w", ns, err)
		}
		for i := range vms.Items {
			vm := &vms.Items[i]
			out[key(ns, vm.Name)] = LiveVM{} // default: exists but no running instance
		}

		vmis, err := c.kubevirt.VirtualMachineInstance(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list VMIs in %s: %w", ns, err)
		}
		for i := range vmis.Items {
			vmi := &vmis.Items[i]
			out[key(ns, vmi.Name)] = liveFromVMI(vmi)
		}
	}
	return out, nil
}

// projectNamespaces returns the namespaces dotvirt manages. With a label
// selector, it queries matching namespaces; without one, it discovers every
// namespace that contains at least one VM (cluster-wide list).
func (c *Client) projectNamespaces(ctx context.Context) ([]string, error) {
	if c.nsLabel != "" {
		nsList, err := c.kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: c.nsLabel})
		if err != nil {
			return nil, fmt.Errorf("list namespaces (%s): %w", c.nsLabel, err)
		}
		names := make([]string, 0, len(nsList.Items))
		for i := range nsList.Items {
			names = append(names, nsList.Items[i].Name)
		}
		return names, nil
	}

	// No selector: find namespaces with VMs via a cluster-wide VM list.
	vms, err := c.kubevirt.VirtualMachine(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list VMs (all namespaces): %w", err)
	}
	seen := map[string]struct{}{}
	var names []string
	for i := range vms.Items {
		ns := vms.Items[i].Namespace
		if _, ok := seen[ns]; !ok {
			seen[ns] = struct{}{}
			names = append(names, ns)
		}
	}
	return names, nil
}

// ListVMObjects returns the VirtualMachine objects in the scoped namespaces,
// for export to the running branch. Callers should strip server-set fields
// before serializing (see export.go).
func (c *Client) ListVMObjects(ctx context.Context) ([]kubevirtcorev1.VirtualMachine, error) {
	namespaces, err := c.projectNamespaces(ctx)
	if err != nil {
		return nil, err
	}
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

func liveFromVMI(vmi *kubevirtcorev1.VirtualMachineInstance) LiveVM {
	live := LiveVM{
		Phase:    string(vmi.Status.Phase),
		NodeName: vmi.Status.NodeName,
	}
	if len(vmi.Status.Interfaces) > 0 {
		live.GuestIP = vmi.Status.Interfaces[0].IP
	}
	for _, cond := range vmi.Status.Conditions {
		if cond.Type == kubevirtcorev1.VirtualMachineInstanceReady {
			live.Ready = cond.Status == corev1.ConditionTrue
		}
	}
	return live
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

func key(ns, name string) string { return ns + "/" + name }

// Key builds the merge key used by callers to look up LiveState entries.
func Key(vm model.VM) string { return key(vm.Namespace, vm.Name) }
