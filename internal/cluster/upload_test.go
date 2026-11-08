package cluster

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
