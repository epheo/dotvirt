package inventory

import (
	"fmt"
	"testing"

	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/project"
)

// BenchmarkBuildLive is the live half of one frame build: 50 repo-less
// projects of one namespace each over 2000 running VMs, plus the adoption scan.
func BenchmarkBuildLive(b *testing.B) {
	live := map[string]clusterstate.LiveVM{}
	var projects []project.ProjectInfo
	var labeled []project.Namespace
	for p := range 50 {
		ns := fmt.Sprintf("ns-%d", p)
		projects = append(projects, project.ProjectInfo{Name: ns, Namespaces: []string{ns}})
		labeled = append(labeled, project.Namespace{Name: ns})
	}
	for i := range 2000 {
		live[fmt.Sprintf("ns-%d/vm-%d", i%50, i)] = clusterstate.LiveVM{Phase: "Running", GuestIP: "10.0.0.5", IPs: []string{"10.0.0.5"}}
	}
	in := Inputs{Projects: projects, Live: live}
	b.ReportAllocs()
	for b.Loop() {
		_ = Build(in)
		_ = Adoptable(live, labeled)
	}
}
