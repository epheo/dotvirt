package argo

import (
	"context"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// Watch streams a coalesced signal on notify whenever an ArgoCD Application
// changes (its status.resources drift may have moved). Runs until ctx is done,
// reconnecting on closure.
func (c *Client) Watch(ctx context.Context, notify chan<- struct{}) {
	go func() {
		for ctx.Err() == nil {
			w, err := c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{})
			if err != nil {
				log.Printf("watch applications: %v; retrying", err)
				if !sleep(ctx, 2*time.Second) {
					return
				}
				continue
			}
			drain(ctx, w, notify)
		}
	}()
}

func drain(ctx context.Context, w watch.Interface, notify chan<- struct{}) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.ResultChan():
			if !ok || ev.Type == watch.Error {
				return
			}
			select {
			case notify <- struct{}{}:
			case <-ctx.Done():
			default:
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
