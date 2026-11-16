package install

import (
	"fmt"
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
// the names, accounts, port, label keys and bot user must stay those.
func TestRenderMatchesTheGuardsPins(t *testing.T) {
	for name, c := range map[string][2]string{
		"console Deployment": {AppName, adminobjects.ConsoleDeployment},
		"console account":    {AppName, adminobjects.ConsoleAccount},
		"forge Deployment":   {ForgejoServiceName, adminobjects.ForgeDeployment},
		"forge account":      {forgejoSAName, adminobjects.ForgeAccount},
		"forge bot":          {ForgejoBotUser, adminobjects.ForgeBotUser},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: install %q, guard %q", name, c[0], c[1])
		}
	}
	args := strings.Join(adminobjects.ConsoleArgs(false), " ")
	for _, want := range []string{fmt.Sprintf("-addr=:%d", HTTPPort), "-project-label=" + ProjectLabel, "-repo-annotation=" + RepoAnnotation} {
		if !strings.Contains(args, want) {
			t.Errorf("console args %q lack %q", args, want)
		}
	}
}
