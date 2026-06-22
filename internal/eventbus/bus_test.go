package eventbus

import "testing"

// drained reports whether ch has a pending wake (and consumes it).
func drained(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestPublishWakesOnlySubscribersOfThatKind(t *testing.T) {
	b := New()
	live, _ := b.Subscribe(LiveChanged)
	git, _ := b.Subscribe(GitChanged)

	b.Publish(GitChanged)
	if drained(live) {
		t.Error("LiveChanged subscriber woke on a GitChanged publish")
	}
	if !drained(git) {
		t.Error("GitChanged subscriber did not wake on a GitChanged publish")
	}
}

func TestSubscribeMultipleKinds(t *testing.T) {
	b := New()
	ch, _ := b.Subscribe(VMSpecChanged, NamespaceChanged)

	b.Publish(NamespaceChanged)
	if !drained(ch) {
		t.Error("did not wake on a subscribed kind (NamespaceChanged)")
	}
	b.Publish(DriftChanged)
	if drained(ch) {
		t.Error("woke on an unsubscribed kind (DriftChanged)")
	}
}

func TestPublishCoalesces(t *testing.T) {
	b := New()
	ch, _ := b.Subscribe(LiveChanged)

	b.Publish(LiveChanged)
	b.Publish(LiveChanged)
	b.Publish(LiveChanged)
	if !drained(ch) {
		t.Fatal("expected a pending wake after publishes")
	}
	if drained(ch) {
		t.Error("expected coalescing to a single wake, found a second")
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(LiveChanged)
	cancel()
	b.Publish(LiveChanged)
	if drained(ch) {
		t.Error("a cancelled subscription still received a wake")
	}
	cancel() // second cancel must be a no-op, not a panic
}

func TestNilBusIsNoop(t *testing.T) {
	var b *Bus
	b.Publish(LiveChanged) // must not panic
	if v := b.Version(LiveChanged); v != 0 {
		t.Errorf("nil bus Version = %d, want 0", v)
	}
	ch, cancel := b.Subscribe(LiveChanged)
	if ch != nil {
		t.Error("nil bus Subscribe should yield a nil channel")
	}
	cancel() // must not panic
}

func TestVersionIncrementsPerKind(t *testing.T) {
	b := New()
	if v := b.Version(VMSpecChanged); v != 0 {
		t.Fatalf("fresh bus Version = %d, want 0", v)
	}
	b.Publish(VMSpecChanged)
	if v := b.Version(VMSpecChanged); v != 1 {
		t.Errorf("after one publish Version(VMSpecChanged) = %d, want 1", v)
	}
	// A different kind doesn't move this one.
	b.Publish(LiveChanged)
	if v := b.Version(VMSpecChanged); v != 1 {
		t.Errorf("Version(VMSpecChanged) moved on a LiveChanged publish: %d", v)
	}
	// The summed version of multiple kinds strictly increases when ANY moves.
	sum := b.Version(VMSpecChanged, LiveChanged)
	b.Publish(LiveChanged)
	if got := b.Version(VMSpecChanged, LiveChanged); got <= sum {
		t.Errorf("summed Version did not increase: %d <= %d", got, sum)
	}
}

// TestVersionBumpedBeforeWake is the load-bearing ordering guarantee: a consumer
// woken by a Publish must observe the incremented version (else it would reconcile
// against stale state and need an extra event to catch up).
func TestVersionBumpedBeforeWake(t *testing.T) {
	b := New()
	ch, _ := b.Subscribe(DriftChanged)
	before := b.Version(DriftChanged)
	b.Publish(DriftChanged)
	if !drained(ch) {
		t.Fatal("expected a wake")
	}
	// The wake fired, so by the bump-before-wake rule the version must already be ahead.
	if got := b.Version(DriftChanged); got <= before {
		t.Errorf("version not bumped before wake: %d <= %d", got, before)
	}
}
