package netstate

import "testing"

// Trace notes reach the UI as written: they name things in its words.
func TestTraceNotesSpeakTheUIsWords(t *testing.T) {
	s := New(nil, nil)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"cidrSelector": "198.51.100.0/24"}},
		}},
	})
	res := s.Trace(wl("team-a", "web", nil, nil, "10.128.2.5"), nil, "203.0.113.9", "TCP", 443)
	var note string
	for _, st := range res.Steps {
		if st.Stage == "gateway" && st.Action == "Allow" {
			note = st.Note
		}
	}
	if note != "No outbound rule matches - outbound traffic is allowed by default." {
		t.Fatalf("outbound default note = %q (%v)", note, stepKinds(t, res))
	}
}
