package adminobjects

import "fmt"

// What the operator's Deployments run with, beyond what an admin chose: the
// operator renders these and the guard pins them, so neither drifts from the
// other.

// ConsolePort is the console container's HTTP port.
const ConsolePort = 8080

// ConsoleAccount and ForgeAccount are the ServiceAccounts the console and the
// managed forge run as; ConsoleDeployment and ForgeDeployment their
// Deployments.
const (
	ConsoleAccount    = "dotvirt"
	ForgeAccount      = "dotvirt-forgejo"
	ConsoleDeployment = "dotvirt"
	ForgeDeployment   = "dotvirt-forgejo"
)

// ForgeBotUser is the managed forge's service user.
const ForgeBotUser = "dotvirt-bot"

// ConsoleArgs are the console container's arguments. insecureTLS is the
// admin's spec.forge.insecureTLS: it also lets the metrics client skip TLS,
// and metrics queries carry each user's token, so only an admin may add it.
func ConsoleArgs(insecureTLS bool) []string {
	args := []string{
		fmt.Sprintf("-addr=:%d", ConsolePort),
		"-ui-origin=", // same-origin: the binary serves the SPA
		"-argo=true",
		"-draft-dir=/var/lib/dotvirt/drafts",
		// Webhooks are the primary trigger for inventory updates; the git poll
		// is only the missed-event backstop, so keep it slow to spare the
		// forge - the managed Forgejo is a single SQLite-backed pod.
		"-git-poll-interval=5m",
		"-project-label=dotvirt.io/project",
		"-repo-annotation=dotvirt.io/repo",
	}
	if insecureTLS {
		args = append(args, "-insecure-tls")
	}
	return args
}

// ForgejoBootstrap is the managed forge's init container command. Every pod
// start converges the bot's password to the current admin secret: the init
// container has local DB access, so it needs no prior password to reset it.
func ForgejoBootstrap() []string {
	return []string{"sh", "-c", `set -e
mkdir -p "$(dirname "$GITEA_APP_INI")"
environment-to-ini
forgejo migrate
forgejo admin user create --admin --username ` + ForgeBotUser +
		` --password "$ADMIN_PW" --email ` + ForgeBotUser + `@dotvirt.local --must-change-password=false ` +
		`|| forgejo admin user change-password --username ` + ForgeBotUser + ` --password "$ADMIN_PW" --must-change-password=false`}
}

// ArgoTarget is the ArgoCD namespace and controller ServiceAccount an install
// writes into: the spec's, else the defaults. The operator and the console
// resolve it alike, so the guard each renders pins the same accounts.
func ArgoTarget(specNamespace, specController string, openShift bool) (namespace, controller string) {
	namespace, controller = specNamespace, specController
	if namespace == "" {
		if openShift {
			namespace = "openshift-gitops"
		} else {
			namespace = "argocd"
		}
	}
	if controller == "" {
		// By the namespace, not the platform: MicroShift reads as OpenShift
		// and runs community Argo CD.
		if namespace == "openshift-gitops" {
			controller = "openshift-gitops-argocd-application-controller"
		} else {
			controller = "argocd-application-controller"
		}
	}
	return namespace, controller
}
