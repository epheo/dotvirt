package cluster

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	return c.createDataVolume(ctx, namespace, name, size, storageClass, map[string]any{"upload": map[string]any{}})
}

// CreateImportDataVolume creates a DataVolume CDI fills by downloading url
// itself: kind model.ImportRegistry (url carries docker://) or
// model.ImportURL (http or https).
func (c *Client) CreateImportDataVolume(ctx context.Context, namespace, name, size, storageClass, kind, url string) error {
	from := "http"
	if kind == model.ImportRegistry {
		from = "registry"
	}
	return c.createDataVolume(ctx, namespace, name, size, storageClass, map[string]any{from: map[string]any{"url": url}})
}

func (c *Client) createDataVolume(ctx context.Context, namespace, name, size, storageClass string, source map[string]any) error {
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
			// On WaitForFirstConsumer storage neither ever starts: an upload
			// waits for a bound claim that waits for a consumer, and a boot
			// image is only ever cloned from, never consumed. Bind now.
			"annotations": map[string]any{"cdi.kubevirt.io/storage.bind.immediate.requested": "true"},
		},
		"spec": map[string]any{
			"source":  source,
			"storage": storage,
		},
	}}
	_, err = dyn.Resource(gvrDataVolumes).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// UploadStatus reads an upload or import DataVolume's phase. Ready
// (UploadReady) means the proxy will accept bytes; Progress is CDI's
// import-progress annotation, present once bytes are flowing.
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
	return model.UploadStatus{Phase: phase, Ready: phase == "UploadReady", Progress: progress, Error: importerError(dv)}, nil
}

// importerError is the import pod's last failure: CDI keeps the pod's
// termination message on the DataVolume's Running condition, reason Error,
// and restarts the pod with backoff, so the phase alone never says.
func importerError(dv *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(dv.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != "Running" || c["status"] != "False" || c["reason"] != "Error" {
			continue
		}
		msg, _ := c["message"].(string)
		return urlQuery.ReplaceAllString(msg, "$1")
	}
	return ""
}

// urlQuery matches a URL's query string: an importer's error quotes the
// source it failed to fetch, pre-signed signature and all.
var urlQuery = regexp.MustCompile(`(\w+://[^\s"'?]*)\?[^\s"']*`)

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

// The annotations a boot image dotvirt imported carries: where it came from,
// as model.ImportFrom shows it, so the Library can stop offering to make a
// boot image of a source that already has one.
const (
	importKindAnnotation   = "dotvirt.io/import-kind"
	importSourceAnnotation = "dotvirt.io/import-source"
)

// CreateBootImage registers the claim the DataVolume name fills as a boot
// image: a DataSource of the same name over it, which the catalog offers once
// the import is ready and a new VM's disk is cloned from. imp, when not nil,
// is the source an import downloaded.
func (c *Client) CreateBootImage(ctx context.Context, namespace, name string, imp *model.DiskImport) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	meta := map[string]any{"name": name, "namespace": namespace}
	if imp != nil {
		meta["annotations"] = map[string]any{importKindAnnotation: imp.Kind, importSourceAnnotation: imp.Source}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataSource",
		"metadata":   meta,
		"spec": map[string]any{
			"source": map[string]any{"pvc": map[string]any{"name": name, "namespace": namespace}},
		},
	}}
	_, err = dyn.Resource(gvrDataSources).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// dataImportCronLabel marks a DataSource a DataImportCron keeps: CDI points
// it at each scheduled import and would bring a deleted one back.
const dataImportCronLabel = "cdi.kubevirt.io/dataImportCron"

// notOwnShape says why ds is not the boot image CreateBootImage makes, ""
// when it is: a DataSource over the claim of its own name, in its own
// namespace, that no DataImportCron keeps.
func notOwnShape(ds *unstructured.Unstructured) string {
	if _, ok := ds.GetLabels()[dataImportCronLabel]; ok {
		return "a scheduled import keeps it"
	}
	name, _, _ := unstructured.NestedString(ds.Object, "spec", "source", "pvc", "name")
	ns, _, _ := unstructured.NestedString(ds.Object, "spec", "source", "pvc", "namespace")
	if name != ds.GetName() || (ns != "" && ns != ds.GetNamespace()) {
		return "it points at another disk"
	}
	return ""
}

// notOwnDisk says why dv is not a disk createDataVolume makes, "" when it
// is: nothing owns it (a VM's disk has its VM as owner), and it is an
// upload or a download.
func notOwnDisk(dv *unstructured.Unstructured) string {
	if refs := dv.GetOwnerReferences(); len(refs) > 0 {
		return fmt.Sprintf("its disk belongs to %s %s", refs[0].Kind, refs[0].Name)
	}
	src, _, _ := unstructured.NestedMap(dv.Object, "spec", "source")
	if len(src) == 1 {
		for k := range src {
			if k == "upload" || k == "http" || k == "registry" {
				return ""
			}
		}
	}
	return "its disk is not an upload or a download"
}

// DeleteBootImage removes a boot image that never finished, under the
// caller's token: the DataSource first, so the catalog stops listing it,
// then the DataVolume of its name, whose claim CDI owns to it and collects
// with it. Only the shape CreateBootImage and createDataVolume make, while
// not Ready, is removed; anything else, a VM's disk above all, is refused
// before the first delete. With the DataVolume already gone nothing vouches
// for a claim of that name, so it is left alone and the note says so.
func (c *Client) DeleteBootImage(ctx context.Context, namespace, name string) (note string, err error) {
	dyn, err := c.dynamic()
	if err != nil {
		return "", err
	}
	ds, err := dyn.Resource(gvrDataSources).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if why := notOwnShape(ds); why != "" {
		return "", fmt.Errorf("%w: %s is not a boot image dotvirt added (%s); nothing was removed", model.ErrConflict, name, why)
	}
	if dataSourceReady(ds) {
		return "", fmt.Errorf("%w: %s is ready, and only an unfinished boot image is deleted here; nothing was removed", model.ErrConflict, name)
	}
	dv, err := dyn.Resource(gvrDataVolumes).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		dv = nil
	case err != nil:
		return "", fmt.Errorf("nothing was removed: %w", err)
	default:
		if why := notOwnDisk(dv); why != "" {
			return "", fmt.Errorf("%w: %s is not a boot image dotvirt added (%s); nothing was removed", model.ErrConflict, name, why)
		}
	}

	// Ready between the read and the delete changes the version.
	rv := ds.GetResourceVersion()
	err = dyn.Resource(gvrDataSources).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	switch {
	case apierrors.IsConflict(err):
		return "", fmt.Errorf("%w: %s changed while it was being deleted; nothing was removed", model.ErrConflict, name)
	case err != nil && !apierrors.IsNotFound(err):
		return "", fmt.Errorf("nothing was removed: %w", err)
	}
	if dv == nil {
		return fmt.Sprintf("Its data volume was already gone, so a claim named %s, if any, was left alone.", name), nil
	}
	// The one read and found unowned, not another made since under its name.
	uid := dv.GetUID()
	err = dyn.Resource(gvrDataVolumes).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("removed the boot image %s; its data volume remains: %w", name, err)
	}
	return "", nil
}
