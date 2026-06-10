// Command dotvirt serves a vCenter-like WebUI that edits a git repo of KubeVirt
// manifests and reads live state from a cluster and ArgoCD.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/epheo/dotvirt/internal/api"
	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/export"
	"github.com/epheo/dotvirt/internal/forge"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/stream"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("dotvirt: %v", err)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}

	if cfg.InsecureTLS {
		git.AllowInsecureTLS() // dev: trust self-signed Forgejo Route cert
	}

	repo, err := git.Open(cfg.RepoURL, cfg.GitUsername, cfg.GitToken)
	if err != nil {
		return err
	}
	provider := git.NewProvider(repo)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// One writable view of the repo, shared by the running-branch exporter and
	// the changeset coordinator (draft → propose → PR).
	writeRepo := git.OpenWrite(cfg.RepoURL, cfg.GitUsername, cfg.GitToken, cfg.Push)

	draftStore, err := draft.Open(cfg.DraftFile)
	if err != nil {
		return err
	}
	forgeClient := forge.New(forge.Config{
		BaseURL: cfg.ForgeURL, Token: cfg.ForgeToken,
		Owner: cfg.ForgeOwner, Repo: cfg.ForgeRepo,
		InsecureTLS: cfg.InsecureTLS,
	})

	// Live inventory hub: watches + a git poll feed its change channel; it pushes
	// recomputed inventory to WebSocket subscribers.
	hub := stream.NewHub(provider.Inventory)
	go hub.Run(ctx)
	go pollGit(ctx, repo, hub.Changed(), cfg.GitPollInterval)

	var vncHandler api.VNCHandler
	var optionsProvider api.OptionsProvider
	if cfg.ClusterEnabled {
		clusterClient, err := cluster.New(cfg.Kubeconfig, cfg.NamespaceLabel)
		if err != nil {
			return err
		}
		provider.WithEnricher(enricher(ctx, clusterClient))
		clusterClient.Watch(ctx, hub.Changed()) // push on VM/VMI changes
		vncHandler = stream.NewVNCProxy(clusterClient)
		optionsProvider = optionsAdapter{clusterClient}

		exporter := export.New(clusterClient, writeRepo, cfg.RunningBranch)
		go exporter.Run(ctx, cfg.ExportInterval)
	}

	var resyncer changeset.Resyncer
	if cfg.ArgoEnabled {
		argoClient, err := argo.New(cfg.Kubeconfig)
		if err != nil {
			return err
		}
		provider.WithDrift(driftSource(ctx, argoClient))
		argoClient.Watch(ctx, hub.Changed()) // push on Application drift changes
		resyncer = resyncAdapter{argoClient}
	}

	coordinator := changeset.New(draftStore, writeRepo, provider, forgeClient, resyncer, cfg.BaseBranch, cfg.ProposedBranch)

	deps := api.Deps{
		AllowOrigin: cfg.UIOrigin,
		Inventory:   provider,
		Options:     optionsProvider,
		Draft:       coordinator,
		Stream:      hub,
		VNC:         vncHandler,
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("dotvirt listening on %s (repo=%s)", cfg.Addr, cfg.RepoURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// pollGit periodically fetches the repo and signals the hub when the set of
// branch heads changes. Git has no watch, so a light poll keeps inventory live
// for feature-branch commits and the running-branch export.
func pollGit(ctx context.Context, repo *git.Repo, changed chan<- struct{}, interval time.Duration) {
	last := ""
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sig, err := repo.HeadsSignature()
			if err != nil {
				continue
			}
			if sig != last {
				last = sig
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}
	}
}

// optionsAdapter adapts the cluster client's typed ListOptions to the API's
// any-returning OptionsProvider interface.
type optionsAdapter struct{ c *cluster.Client }

func (a optionsAdapter) ListOptions(ctx context.Context) (any, error) {
	return a.c.ListOptions(ctx)
}

// resyncAdapter adapts the argo client's typed Resync to changeset.Resyncer.
type resyncAdapter struct{ c *argo.Client }

func (a resyncAdapter) Resync(ctx context.Context, namespace, name string) (any, error) {
	return a.c.Resync(ctx, namespace, name)
}

// enricher adapts the cluster client's live state to the inventory provider's
// Enricher signature.
func enricher(ctx context.Context, c *cluster.Client) git.Enricher {
	return func() (map[string]git.LiveState, error) {
		live, err := c.LiveState(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]git.LiveState, len(live))
		for k, v := range live {
			out[k] = git.LiveState{Phase: v.Phase, GuestIP: v.GuestIP, NodeName: v.NodeName}
		}
		return out, nil
	}
}

// driftSource adapts the argo client's drift map to the inventory provider's
// DriftSource signature.
func driftSource(ctx context.Context, c *argo.Client) git.DriftSource {
	return func() (map[string]git.Drift, error) {
		drift, err := c.VMDrift(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]git.Drift, len(drift))
		for k, v := range drift {
			out[k] = git.Drift{Sync: v.Sync, Health: v.Health}
		}
		return out, nil
	}
}
