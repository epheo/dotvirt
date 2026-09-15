package netgen

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A tenant filter hides a cluster row only when its namespaces are provably
// enumerated; every ambiguous selector must come out nil so the row stays.
func TestNamespaceNames(t *testing.T) {
	nameIn := func(vals ...string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: NamespaceNameLabel, Operator: metav1.LabelSelectorOpIn, Values: vals,
		}}}
	}
	cases := []struct {
		name string
		sel  *metav1.LabelSelector
		want []string
	}{
		{"nil selector", nil, nil},
		{"empty selector", &metav1.LabelSelector{}, nil},
		{"name-In", nameIn("a", "b"), []string{"a", "b"}},
		{"name-In empty values", nameIn(), nil},
		{"name matchLabel", &metav1.LabelSelector{MatchLabels: map[string]string{NamespaceNameLabel: "a"}}, []string{"a"}},
		{"other matchLabel", &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}, nil},
		{"name-NotIn", &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: NamespaceNameLabel, Operator: metav1.LabelSelectorOpNotIn, Values: []string{"a"},
		}}}, nil},
		{"name-In plus label", func() *metav1.LabelSelector {
			s := nameIn("a")
			s.MatchLabels = map[string]string{"env": "prod"}
			return s
		}(), nil},
	}
	for _, c := range cases {
		got := NamespaceNames(c.sel)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}
