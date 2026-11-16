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
