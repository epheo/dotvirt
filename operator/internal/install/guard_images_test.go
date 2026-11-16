package install

import (
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// The guard admits the official repositories; the operator's built-in
// defaults must come from them, or an install on defaults is refused.
func TestDefaultImagesAreInTheGuardRepositories(t *testing.T) {
	for _, img := range []string{defaultImage, forgejoImage} {
		repo, _, _ := strings.Cut(img, "@")
		repo, _, _ = strings.Cut(repo, ":")
		if !slices.Contains(adminobjects.Repositories, repo) {
			t.Errorf("default image %s is outside the guard's repositories %v", img, adminobjects.Repositories)
		}
	}
}

// The guard pins what the operator renders by the shared package's values:
// the names and accounts of its Deployments, and the metrics CA paths.
func TestRenderMatchesTheGuardsPins(t *testing.T) {
	for name, c := range map[string][2]string{
		"console Deployment": {AppName, adminobjects.ConsoleDeployment},
		"console account":    {AppName, adminobjects.ConsoleAccount},
		"forge Deployment":   {ForgejoServiceName, adminobjects.ForgeDeployment},
		"forge account":      {forgejoSAName, adminobjects.ForgeAccount},
		"metrics CA":         {metricsCAMountPath + "/" + metricsCAFile, adminobjects.MetricsCAPath},
		"service CA":         {serviceCAMountPath + "/" + ServiceCAKey, adminobjects.ServiceCAPath},
		"router CA":          {ingressCAMountPath + "/" + IngressCAKey, adminobjects.IngressCAPath},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: install %q, guard %q", name, c[0], c[1])
		}
	}
}
