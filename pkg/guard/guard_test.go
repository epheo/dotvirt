package guard

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

var params = Params{Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt", ArgoNamespace: "openshift-gitops"}

func exprs(p *admissionregistrationv1.ValidatingAdmissionPolicy) []string {
	var out []string
	for _, v := range p.Spec.Validations {
		out = append(out, v.Expression)
	}
	return out
}

func has(t *testing.T, where string, got []string, needle string) {
	t.Helper()
	if !slices.ContainsFunc(got, func(e string) bool { return strings.Contains(e, needle) }) {
		t.Errorf("%s: no rule holds %s; got %q", where, needle, got)
	}
}

// The render pins the operator's exact username and the install's namespaces,
// and matches only the two kinds it bounds, created or updated.
func TestRenderPinsTheOperatorAndItsNamespaces(t *testing.T) {
	s := Render(params)
	if len(s.Policies) != 2 || len(s.Bindings) != 2 {
		t.Fatalf("render = %d policies, %d bindings", len(s.Policies), len(s.Bindings))
	}
	for i, want := range []struct{ name, group, resource string }{{SecretsName, "", "secrets"}, {DeploymentsName, "apps", "deployments"}} {
		p := s.Policies[i]
		if p.Name != want.name || *p.Spec.FailurePolicy != admissionregistrationv1.Fail {
			t.Errorf("policy %d = %s", i, p.Name)
		}
		rules := p.Spec.MatchConstraints.ResourceRules
		if len(rules) != 1 || !slices.Equal(rules[0].APIGroups, []string{want.group}) || !slices.Equal(rules[0].Resources, []string{want.resource}) ||
			!slices.Equal(rules[0].Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}) {
			t.Errorf("%s match = %+v", p.Name, rules)
		}
		if len(p.Spec.MatchConditions) != 1 || p.Spec.MatchConditions[0].Expression != `request.userInfo.username == "`+params.Operator+`"` {
			t.Errorf("%s condition = %+v, want the exact username", p.Name, p.Spec.MatchConditions)
		}
		for _, v := range p.Spec.Validations {
			if !strings.HasSuffix(v.Message, "(dotvirt operator guard)") {
				t.Errorf("%s message %q does not say which guard refused", p.Name, v.Message)
			}
		}
		b := s.Bindings[i]
		if b.Name != want.name || b.Spec.PolicyName != want.name || b.Spec.MatchResources != nil ||
			!slices.Equal(b.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
			t.Errorf("binding %s = %+v", b.Name, b.Spec)
		}
	}
	sec, dep := exprs(s.Policies[0]), exprs(s.Policies[1])
	has(t, "secrets", sec, `object.type != "kubernetes.io/service-account-token"`)
	has(t, "secrets", sec, `"kubernetes.io/service-account.name" in object.metadata.annotations`)
	has(t, "secrets", sec, `request.namespace in ["dotvirt", "openshift-gitops"]`)
	has(t, "deployments", dep, `request.namespace == "dotvirt"`)
	has(t, "deployments", dep, `serviceAccountName in ["dotvirt", "dotvirt-forgejo"]`)
	has(t, "deployments", dep, `hostNetwork`)
	has(t, "deployments", dep, `hostPID`)
	has(t, "deployments", dep, `hostIPC`)
	has(t, "deployments", dep, `!has(v.hostPath)`)
	has(t, "deployments", dep, `containers.all(c, `+unprivileged)
	has(t, "deployments", dep, `initContainers.all(c, `+unprivileged)
	for _, needle := range []string{"privileged", "allowPrivilegeEscalation"} {
		if !strings.Contains(unprivileged, needle) {
			t.Errorf("container rule misses %s", needle)
		}
	}
}

func TestApplyCommandIsOneQuotedLine(t *testing.T) {
	cmd, err := Render(params).ApplyCommand()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd, "\n") || strings.Count(cmd, "'") != 2 || !strings.Contains(cmd, "kubectl apply --server-side") {
		t.Errorf("command = %s", cmd)
	}
	for _, name := range Names {
		if !strings.Contains(cmd, `"name":"`+name+`"`) {
			t.Errorf("command misses %s", name)
		}
	}
}

func live(s Set) (map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	ps := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	bs := map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	for _, p := range s.Policies {
		ps[p.Name] = p.DeepCopy()
	}
	for _, b := range s.Bindings {
		bs[b.Name] = b.DeepCopy()
	}
	return ps, bs
}

func TestCompare(t *testing.T) {
	want := Render(params)
	moved := params
	moved.ArgoNamespace = "argocd"
	for name, c := range map[string]struct {
		edit func(map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding)
		want State
	}{
		"as rendered": {func(map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
		}, Installed},
		"a binding missing": {func(_ map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, b map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			delete(b, DeploymentsName)
		}, NotInstalled},
		"rendered for another Argo namespace": {func(p map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, b map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			o := Render(moved)
			p[SecretsName], b[SecretsName] = o.Policies[0], o.Bindings[0]
		}, Outdated},
		"a validation dropped": {func(p map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, _ map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			p[DeploymentsName].Spec.Validations = p[DeploymentsName].Spec.Validations[1:]
		}, Modified},
		"the condition widened": {func(p map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, _ map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			p[SecretsName].Spec.MatchConditions[0].Expression = "true"
		}, Modified},
		"a warn-only binding": {func(_ map[string]*admissionregistrationv1.ValidatingAdmissionPolicy, b map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			b[SecretsName].Spec.ValidationActions = []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn}
		}, Modified},
	} {
		ps, bs := live(want)
		c.edit(ps, bs)
		if got, detail := Compare(want, ps, bs); got != c.want {
			t.Errorf("%s: %s (%s), want %s", name, got, detail, c.want)
		}
	}
}

func TestUsernameFromToken(t *testing.T) {
	enc := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	if got, err := UsernameFromToken(enc(`{"sub":"system:serviceaccount:ops:op"}`) + "\n"); err != nil || got != "system:serviceaccount:ops:op" {
		t.Errorf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "a.b", enc(`{"sub":"alice"}`), enc(`{"sub":"system:serviceaccount:ops"}`), enc(`not json`)} {
		if got, err := UsernameFromToken(bad); err == nil {
			t.Errorf("%q: got %q, want an error", bad, got)
		}
	}
}
