package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The catalog's cluster-scoped kinds, each shown to a caller who may list it
// cluster-wide. KubeVirt (instancetype.kubevirt.io:view) and CDI
// (cdi.kubevirt.io:config-reader) grant the first three to every
// authenticated user wherever they run; storage classes come from OpenShift's
// basic-user or, elsewhere, the operator's dotvirt-storage-catalog.
var (
	ssarInstancetypes   = ssarRef{"instancetype.kubevirt.io", "virtualmachineclusterinstancetypes"}
	ssarPreferences     = ssarRef{"instancetype.kubevirt.io", "virtualmachineclusterpreferences"}
	ssarStorageProfiles = ssarRef{"cdi.kubevirt.io", "storageprofiles"}
	ssarStorageClasses  = ssarRef{"storage.k8s.io", "storageclasses"}
	ssarSnapshotClasses = ssarRef{"snapshot.storage.k8s.io", "volumesnapshotclasses"}
	// Namespaced, but asked cluster-wide: free capacity is a sum over every
	// segment, and a partial sum would misstate it.
	ssarCSICapacities = ssarRef{"storage.k8s.io", "csistoragecapacities"}
)

// handleStorageClasses serves the Storage section's class fact sheet, read
// once with dotvirt's SA and cached (short: free capacity moves). Each class's
// CDI profile, and the snapshot class it names, show when the caller may list
// StorageProfiles; the provisioner's VolumeSnapshotClass stands in for a
// profile that names none when the caller may list those; free capacity when
// they may list CSIStorageCapacities. A capacity segment's topology is
// usually a node's hostname, so the per-segment split also needs nodes. The
// classes themselves need list on StorageClasses. The cached sheet is never
// mutated.
func (s *Server) handleStorageClasses(w http.ResponseWriter, r *http.Request) {
	id, c, classes, ok := saCached(s, w, r, s.storage, (*cluster.Client).ListStorageClasses)
	if !ok {
		return
	}
	out := make([]model.StorageClassInfo, 0, len(classes))
	if len(classes) == 0 {
		writeJSON(w, out)
		return
	}
	// Cluster-wide questions only, so no view; a failed review reads as no.
	lists, _, _ := s.askEach(r.Context(), id, c, []question{
		refQ(verbList, ssarStorageClasses, ""),
		refQ(verbList, ssarStorageProfiles, ""),
		refQ(verbList, ssarSnapshotClasses, ""),
		refQ(verbList, ssarCSICapacities, ""),
		refQ(verbList, ssarNodes, ""),
	})
	if !lists[0] {
		writeJSON(w, out)
		return
	}
	profiles, snapshots, free, nodes := lists[1], lists[2], lists[3], lists[4]
	for _, sc := range classes {
		c := sc.StorageClassInfo
		if !profiles {
			c.Profile, c.SnapshotClass = nil, ""
		}
		if c.SnapshotClass == "" && snapshots {
			c.SnapshotClass = sc.SnapshotFallback
		}
		if !free {
			c.Free = nil
		}
		if !free || !nodes {
			c.Segments = nil
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

// handleStorageSnapshots lists the VM snapshots across the caller's managed
// namespaces (narrowed by ?project= or ?namespace=): the Storage section's
// Snapshots tab and each disk's last snapshot. A user-opened view, read under
// the caller's token like the events lane, never from the broadcast path.
func (s *Server) handleStorageSnapshots(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	snaps, err := sc.cluster.ListVMSnapshots(r.Context(), nss)
	respond(w, snaps, err)
}

// handleStorageClaims lists the claims behind VM disks in the same scope: the
// host a local disk is bound to stays readable while its VM is stopped.
func (s *Server) handleStorageClaims(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	claims, err := sc.cluster.ListClaims(r.Context(), nss)
	respond(w, claims, err)
}
