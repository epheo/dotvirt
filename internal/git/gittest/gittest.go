// Package gittest serves bare repos over git's smart HTTP protocol, for tests
// that must see what a clone or push sends: its address and credentials.
package gittest

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Handler serves the bare repos under root through git-http-backend, pushes
// included, at /<path under root>. It checks no credentials: a test wraps it
// to record or refuse them. An error means this host has no backend to run.
func Handler(root string) (http.Handler, error) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return nil, fmt.Errorf("git --exec-path: %w", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		return nil, fmt.Errorf("no git-http-backend: %w", err)
	}
	return &cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			// The backend takes pushes only from an authenticated user or
			// where the repo allows them.
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.receivepack", "GIT_CONFIG_VALUE_0=true",
		},
	}, nil
}
