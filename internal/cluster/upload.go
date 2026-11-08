package cluster

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

// Image upload: a CDI upload-source DataVolume is the
// target PVC, and an UploadTokenRequest mints the bearer the browser presents
// to cdi-uploadproxy when it streams the image bytes directly. Both go through
// the dynamic client under the user's token - uploading a golden image is an
// imperative, RBAC-gated op (like snapshots/clones), not git-managed state.
var (
	gvrDataVolumes  = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}
	gvrUploadTokens = schema.GroupVersionResource{Group: "upload.cdi.kubevirt.io", Version: "v1beta1", Resource: "uploadtokenrequests"}
	gvrCDIConfigs   = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "cdiconfigs"}
)

// UploadProxyURL is the cdi-uploadproxy base CDI publishes for clients outside
// the cluster: an admin's override, else the route CDI found. CDI reports a
// bare host, which browsers reach over https. Empty when CDI has none.
func (c *Client) UploadProxyURL(ctx context.Context) (string, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return "", err
	}
	cfg, err := dyn.Resource(gvrCDIConfigs).Get(ctx, "config", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	url, _, _ := unstructured.NestedString(cfg.Object, "status", "uploadProxyURL")
	if url != "" && !strings.Contains(url, "://") {
		url = "https://" + url
	}
	return url, nil
}

// CreateUploadDataVolume creates the upload-target DataVolume (source: upload)
// in namespace under the caller's token. The browser streams into the PVC this
// provisions once it reaches UploadReady. An empty storageClass uses the
// cluster default.
func (c *Client) CreateUploadDataVolume(ctx context.Context, namespace, name, size, storageClass string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	storage := map[string]any{
		"resources": map[string]any{"requests": map[string]any{"storage": size}},
	}
	if storageClass != "" {
		storage["storageClassName"] = storageClass
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			// WaitForFirstConsumer storage would deadlock an upload DV: the PVC
			// waits for a consumer, but the upload pod waits for the bound PVC.
			// Force immediate binding so it reaches UploadReady.
			"annotations": map[string]any{"cdi.kubevirt.io/storage.bind.immediate.requested": "true"},
		},
		"spec": map[string]any{
			"source":  map[string]any{"upload": map[string]any{}},
			"storage": storage,
		},
	}}
	_, err = dyn.Resource(gvrDataVolumes).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// UploadStatus reads the upload DataVolume's phase. Ready (UploadReady) means
// the proxy will accept bytes; Progress is CDI's import-progress annotation,
// present once bytes are flowing.
func (c *Client) UploadStatus(ctx context.Context, namespace, name string) (model.UploadStatus, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return model.UploadStatus{}, err
	}
	dv, err := dyn.Resource(gvrDataVolumes).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return model.UploadStatus{}, err
	}
	phase, _, _ := unstructured.NestedString(dv.Object, "status", "phase")
	progress, _, _ := unstructured.NestedString(dv.Object, "status", "progress")
	return model.UploadStatus{Phase: phase, Ready: phase == "UploadReady", Progress: progress}, nil
}

// CreateUploadToken mints an UploadTokenRequest for the upload PVC and returns
// the bearer the browser sends to the proxy. The aggregated API populates the
// token synchronously in the response's status.
func (c *Client) CreateUploadToken(ctx context.Context, namespace, name string) (string, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return "", err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "upload.cdi.kubevirt.io/v1beta1",
		"kind":       "UploadTokenRequest",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"pvcName": name},
	}}
	res, err := dyn.Resource(gvrUploadTokens).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	token, ok, _ := unstructured.NestedString(res.Object, "status", "token")
	if !ok || token == "" {
		return "", fmt.Errorf("upload token not issued for %s/%s", namespace, name)
	}
	return token, nil
}

// CreateBootImage registers the upload's claim as a boot image: a DataSource
// of the same name over it, which the catalog offers once the import is
// ready and a new VM's disk is cloned from.
func (c *Client) CreateBootImage(ctx context.Context, namespace, name string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataSource",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"source": map[string]any{"pvc": map[string]any{"name": name, "namespace": namespace}},
		},
	}}
	_, err = dyn.Resource(gvrDataSources).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}
