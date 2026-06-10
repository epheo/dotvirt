package cluster

import (
	"context"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// Watch streams a coalesced signal on notify whenever a VirtualMachine or
// VirtualMachineInstance changes in the scoped namespaces. Callers get "something
// changed" (not the event), since they recompute the full inventory. It runs
// until ctx is cancelled, reconnecting on watch closure.
func (c *Client) Watch(ctx context.Context, notify chan<- struct{}) {
	go c.watchLoop(ctx, "VirtualMachine", notify, func(ctx context.Context) (watch.Interface, error) {
		return c.kubevirt.VirtualMachine(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{})
	})
	go c.watchLoop(ctx, "VirtualMachineInstance", notify, func(ctx context.Context) (watch.Interface, error) {
		return c.kubevirt.VirtualMachineInstance(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{})
	})
}

func (c *Client) watchLoop(ctx context.Context, kind string, notify chan<- struct{}, open func(context.Context) (watch.Interface, error)) {
	for ctx.Err() == nil {
		w, err := open(ctx)
		if err != nil {
			log.Printf("watch %s: %v; retrying", kind, err)
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		drain(ctx, w, notify)
	}
}

// drain forwards a signal for each event until the watch channel closes.
func drain(ctx context.Context, w watch.Interface, notify chan<- struct{}) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.ResultChan():
			if !ok {
				return // server closed the watch; caller reconnects
			}
			if ev.Type == watch.Error {
				return
			}
			signal(ctx, notify)
		}
	}
}

func signal(ctx context.Context, notify chan<- struct{}) {
	select {
	case notify <- struct{}{}:
	case <-ctx.Done():
	default: // a signal is already pending; coalesce
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
