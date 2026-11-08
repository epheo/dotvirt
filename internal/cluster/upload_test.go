package cluster

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/epheo/dotvirt/internal/model"
)

// CDI reports its upload proxy as a bare host; browsers stream to it over https.
func TestUploadProxyURLReadsCDIConfig(t *testing.T) {
	ctx := context.Background()
	for published, want := range map[string]string{
		"cdi-uploadproxy-openshift-cnv.apps.example": "https://cdi-uploadproxy-openshift-cnv.apps.example",
		"https://upload.example:8443":                "https://upload.example:8443",
		"":                                           "",
	} {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		cfg := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "CDIConfig",
			"metadata": map[string]any{"name": "config"},
			"status":   map[string]any{"uploadProxyURL": published},
		}}
		if _, err := dyn.Resource(gvrCDIConfigs).Create(ctx, cfg, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		got, err := NewClient(nil, nil, dyn).UploadProxyURL(ctx)
		if err != nil || got != want {
			t.Errorf("published %q: got %q, %v; want %q", published, got, err, want)
		}
	}
}

// Without CDI there is nothing to read, and the caller keeps upload off.
func TestUploadProxyURLWithoutCDI(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if got, err := NewClient(nil, nil, dyn).UploadProxyURL(context.Background()); err == nil || got != "" {
		t.Errorf("got %q, %v; want an error and no URL", got, err)
	}
}

// An import is a DataVolume CDI fills itself and a DataSource over it that
// names, as shown, where it came from: the catalog reads that back so the
// Library stops offering a boot image of a source that has one.
func TestImportBootImage(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrInstancetypes:  "VirtualMachineClusterInstancetypeList",
		gvrPreferences:    "VirtualMachineClusterPreferenceList",
		gvrDataSources:    "DataSourceList",
		gvrNADs:           "NetworkAttachmentDefinitionList",
		gvrStorageClasses: "StorageClassList",
	})
	c := NewClient(nil, nil, dyn)
	const url = "https://images.example/noble.img?X-Amz-Signature=abc"
	if err := c.CreateImportDataVolume(ctx, "alpha", "noble", "20Gi", "", model.ImportURL, url); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateImportDataVolume(ctx, "alpha", "fedora", "30Gi", "fast", model.ImportRegistry, "docker://quay.io/containerdisks/fedora:42"); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateBootImage(ctx, "alpha", "noble", model.ImportFrom(model.ImportURL, url)); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateBootImage(ctx, "alpha", "uploaded", nil); err != nil {
		t.Fatal(err)
	}

	dv, err := dyn.Resource(gvrDataVolumes).Namespace("alpha").Get(ctx, "noble", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(dv.Object, "spec", "source", "http", "url"); got != url {
		t.Errorf("http source = %q, want the url as given", got)
	}
	if dv.GetAnnotations()["cdi.kubevirt.io/storage.bind.immediate.requested"] != "true" {
		t.Error("a boot image is never consumed: its claim must bind at once")
	}
	dv, _ = dyn.Resource(gvrDataVolumes).Namespace("alpha").Get(ctx, "fedora", metav1.GetOptions{})
	if got, _, _ := unstructured.NestedString(dv.Object, "spec", "source", "registry", "url"); got != "docker://quay.io/containerdisks/fedora:42" {
		t.Errorf("registry source = %q", got)
	}
	if got, _, _ := unstructured.NestedString(dv.Object, "spec", "storage", "storageClassName"); got != "fast" {
		t.Errorf("storage class = %q", got)
	}

	opts, err := c.ListOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*model.DiskImport{}
	for _, img := range opts.OSImages {
		got[img.Name] = img.Import
	}
	if imp := got["noble"]; imp == nil || *imp != (model.DiskImport{Kind: model.ImportURL, Source: "https://images.example/noble.img"}) {
		t.Errorf("imported boot image = %+v", imp)
	}
	if imp, ok := got["uploaded"]; !ok || imp != nil {
		t.Errorf("uploaded boot image = %+v, %v; want no import", imp, ok)
	}
}

// CDI retries a failing importer with backoff and keeps the phase: the
// failure lives on the Running condition, quoted without its query string.
func TestUploadStatusReadsTheImporterError(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dv := func(name string, cond map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataVolume",
			"metadata": map[string]any{"name": name, "namespace": "alpha"},
			"status": map[string]any{
				"phase":      "ImportInProgress",
				"progress":   "N/A",
				"conditions": []any{map[string]any{"type": "Bound", "status": "True"}, cond},
			},
		}}
	}
	for _, o := range []*unstructured.Unstructured{
		dv("broken", map[string]any{"type": "Running", "status": "False", "reason": "Error",
			"message": `Unable to connect to http data source: Get "https://images.example/a.img?X-Amz-Signature=abc&t=1": dial tcp: no such host`}),
		dv("running", map[string]any{"type": "Running", "status": "True", "reason": "Pod is running"}),
		dv("done", map[string]any{"type": "Running", "status": "False", "reason": "Completed", "message": "Import Complete"}),
	} {
		if _, err := dyn.Resource(gvrDataVolumes).Namespace("alpha").Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c := NewClient(nil, nil, dyn)
	for name, want := range map[string]string{
		"broken":  `Unable to connect to http data source: Get "https://images.example/a.img": dial tcp: no such host`,
		"running": "",
		"done":    "",
	} {
		st, err := c.UploadStatus(ctx, "alpha", name)
		if err != nil || st.Error != want || st.Phase != "ImportInProgress" {
			t.Errorf("%s: %+v, %v; want error %q", name, st, err, want)
		}
	}
}
