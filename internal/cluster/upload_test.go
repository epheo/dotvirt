package cluster

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

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
	for _, img := range opts.OSImages {
		if !img.Removable {
			t.Errorf("%s: not removable, want the shape dotvirt makes to be", img.Name)
		}
	}
}

// The catalog marks the boot images DeleteBootImage would take: a scheduled
// import's, or one over another claim, is not.
func TestOptionsMarkRemovable(t *testing.T) {
	ctx := context.Background()
	var objs []runtime.Object
	for _, name := range []string{"own", "cron", "elsewhere"} {
		objs = append(objs, bootImageObjects(name, false, func(ds, _ map[string]any) {
			switch name {
			case "cron":
				ds["metadata"].(map[string]any)["labels"] = map[string]any{"cdi.kubevirt.io/dataImportCron": "x"}
			case "elsewhere":
				ds["spec"] = map[string]any{"source": map[string]any{"pvc": map[string]any{"name": "golden", "namespace": "alpha"}}}
			}
		})[0])
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrInstancetypes:  "VirtualMachineClusterInstancetypeList",
		gvrPreferences:    "VirtualMachineClusterPreferenceList",
		gvrDataSources:    "DataSourceList",
		gvrNADs:           "NetworkAttachmentDefinitionList",
		gvrStorageClasses: "StorageClassList",
	}, objs...)
	opts, err := NewClient(nil, nil, dyn).ListOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, img := range opts.OSImages {
		got[img.Name] = img.Removable
	}
	if want := map[string]bool{"own": true, "cron": false, "elsewhere": false}; !maps.Equal(got, want) {
		t.Errorf("removable = %v, want %v", got, want)
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

// bootImageObjects are what an import leaves: a DataSource over the claim
// of its name, the DataVolume filling it, and the claim. ready marks the
// DataSource Ready; edit changes the DataSource and the DataVolume before
// they are stored.
func bootImageObjects(name string, ready bool, edit func(ds, dv map[string]any)) []runtime.Object {
	status := "False"
	if ready {
		status = "True"
	}
	ds := map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataSource",
		"metadata": map[string]any{"name": name, "namespace": "alpha", "resourceVersion": "7"},
		"spec":     map[string]any{"source": map[string]any{"pvc": map[string]any{"name": name, "namespace": "alpha"}}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": status}}},
	}
	dv := map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataVolume",
		"metadata": map[string]any{"name": name, "namespace": "alpha", "uid": "dv-uid"},
		"spec":     map[string]any{"source": map[string]any{"http": map[string]any{"url": "https://images.example/" + name + ".img"}}},
	}
	if edit != nil {
		edit(ds, dv)
	}
	return []runtime.Object{
		&unstructured.Unstructured{Object: ds},
		&unstructured.Unstructured{Object: dv},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "PersistentVolumeClaim",
			"metadata": map[string]any{"name": name, "namespace": "alpha"},
		}},
	}
}

var gvrPVC = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}

// deletes are the resources dyn was asked to delete, in order.
func deletes(dyn *dynamicfake.FakeDynamicClient) []string {
	var out []string
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "delete" {
			out = append(out, a.GetResource().Resource)
		}
	}
	return out
}

// An unfinished boot image goes in order: the DataSource, so the catalog
// drops it, then the DataVolume, whose claim CDI collects with it; the
// claim itself is never deleted. The DataSource delete is pinned to the
// version read, so one that turned Ready since stays, and the DataVolume
// delete to the one found unowned.
func TestDeleteBootImage(t *testing.T) {
	ctx := context.Background()
	objs := bootImageObjects("noble", false, nil)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	note, err := NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble")
	if err != nil || note != "" {
		t.Fatalf("got %q, %v", note, err)
	}
	if got, want := deletes(dyn), []string{"datasources", "datavolumes"}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v", got, want)
	}
	for _, a := range dyn.Actions() {
		d, ok := a.(k8stesting.DeleteAction)
		if !ok {
			continue
		}
		p := d.GetDeleteOptions().Preconditions
		switch a.GetResource().Resource {
		case "datasources":
			if p == nil || p.ResourceVersion == nil || *p.ResourceVersion != "7" {
				t.Errorf("datasource delete preconditions = %+v, want the version read", p)
			}
		case "datavolumes":
			if p == nil || p.UID == nil || *p.UID != "dv-uid" {
				t.Errorf("datavolume delete preconditions = %+v, want the uid read", p)
			}
		}
	}

	// The DataVolume already gone: the DataSource goes, a claim of its name
	// stays, and the note says so.
	dyn = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs[0], objs[2])
	note, err = NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble")
	if err != nil || !strings.Contains(note, "a claim named noble, if any, was left alone") {
		t.Fatalf("got %q, %v; want the claim left alone, said", note, err)
	}
	if got := deletes(dyn); !slices.Equal(got, []string{"datasources"}) {
		t.Errorf("deleted %v, want the data source alone", got)
	}
	if _, err := dyn.Resource(gvrPVC).Namespace("alpha").Get(ctx, "noble", metav1.GetOptions{}); err != nil {
		t.Errorf("claim gone: %v", err)
	}

	// Gone between the read and the delete: no failure.
	dyn = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	dyn.PrependReactor("delete", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(a.GetResource().GroupResource(), "noble")
	})
	if _, err := NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble"); err != nil {
		t.Errorf("not found on delete: %v", err)
	}
}

// Anything but the unfinished shape dotvirt makes is refused before the
// first delete: a ready boot image, one over another claim or another
// namespace's, one a DataImportCron keeps, a disk a VM owns, a disk cloned
// rather than uploaded or downloaded, one that does not exist.
func TestDeleteBootImageRefuses(t *testing.T) {
	ctx := context.Background()
	pvc := func(name, ns string) func(ds, dv map[string]any) {
		return func(ds, _ map[string]any) {
			ds["spec"] = map[string]any{"source": map[string]any{"pvc": map[string]any{"name": name, "namespace": ns}}}
		}
	}
	for name, c := range map[string]struct {
		ready bool
		edit  func(ds, dv map[string]any)
		want  string
	}{
		"ready":           {ready: true, want: "noble is ready"},
		"other claim":     {edit: pvc("fedora", "alpha"), want: "points at another disk"},
		"other namespace": {edit: pvc("noble", "os-images"), want: "points at another disk"},
		"snapshot source": {edit: func(ds, _ map[string]any) {
			ds["spec"] = map[string]any{"source": map[string]any{"snapshot": map[string]any{"name": "noble", "namespace": "alpha"}}}
		}, want: "points at another disk"},
		"cron": {edit: func(ds, _ map[string]any) {
			ds["metadata"].(map[string]any)["labels"] = map[string]any{"cdi.kubevirt.io/dataImportCron": "noble-cron"}
		}, want: "a scheduled import keeps it"},
		"owned disk": {edit: func(_, dv map[string]any) {
			dv["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
				"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine", "name": "web-1", "uid": "vm-uid",
			}}
		}, want: "its disk belongs to VirtualMachine web-1"},
		"cloned disk": {edit: func(_, dv map[string]any) {
			dv["spec"] = map[string]any{"source": map[string]any{"pvc": map[string]any{"name": "golden", "namespace": "alpha"}}}
		}, want: "its disk is not an upload or a download"},
		"no source": {edit: func(_, dv map[string]any) {
			dv["spec"] = map[string]any{"sourceRef": map[string]any{"kind": "DataSource", "name": "fedora"}}
		}, want: "its disk is not an upload or a download"},
	} {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), bootImageObjects("noble", c.ready, c.edit)...)
		_, err := NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble")
		if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing was removed") {
			t.Errorf("%s: %v, want a conflict saying %q", name, err, c.want)
		}
		if got := deletes(dyn); len(got) != 0 {
			t.Errorf("%s: deleted %v", name, got)
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if _, err := NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble"); !apierrors.IsNotFound(err) {
		t.Errorf("missing: %v, want not found", err)
	}
}

// A delete that fails says what went and what stays, and keeps the
// apiserver's status for the route to answer with.
func TestDeleteBootImagePartial(t *testing.T) {
	ctx := context.Background()
	for resource, want := range map[string]string{
		"datasources": "nothing was removed: ",
		"datavolumes": "removed the boot image noble; its data volume remains: ",
	} {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), bootImageObjects("noble", false, nil)...)
		dyn.PrependReactor("delete", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "noble", errors.New("no"))
		})
		_, err := NewClient(nil, nil, dyn).DeleteBootImage(ctx, "alpha", "noble")
		if !apierrors.IsForbidden(err) || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s fails: %v, want %q and forbidden", resource, err, want)
		}
		if got := deletes(dyn); got[len(got)-1] != resource {
			t.Errorf("%s fails: deleted %v, want it to stop there", resource, got)
		}
	}
}
