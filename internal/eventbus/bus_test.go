package eventbus

import (
	"testing"
	"time"
)

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
	ch, _ := b.Subscribe(NetworkChanged, NamespaceChanged)

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
	if v := b.Version(NetworkChanged); v != 0 {
		t.Fatalf("fresh bus Version = %d, want 0", v)
	}
	b.Publish(NetworkChanged)
	if v := b.Version(NetworkChanged); v != 1 {
		t.Errorf("after one publish Version(NetworkChanged) = %d, want 1", v)
	}
	// A different kind doesn't move this one.
	b.Publish(LiveChanged)
	if v := b.Version(NetworkChanged); v != 1 {
		t.Errorf("Version(NetworkChanged) moved on a LiveChanged publish: %d", v)
	}
	// The summed version of multiple kinds strictly increases when ANY moves.
	sum := b.Version(NetworkChanged, LiveChanged)
	b.Publish(LiveChanged)
	if got := b.Version(NetworkChanged, LiveChanged); got <= sum {
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

func TestPublishKeyMovesOnlyThatKey(t *testing.T) {
	b := New()
	ch, _ := b.Subscribe(GitChanged)
	a0, b0, total := b.KeyVersion(GitChanged, "a"), b.KeyVersion(GitChanged, "b"), b.Version(GitChanged)

	b.PublishKey(GitChanged, "a")
	if !drained(ch) {
		t.Fatal("a keyed publish must wake the kind's subscribers")
	}
	if b.KeyVersion(GitChanged, "a") == a0 {
		t.Error("KeyVersion(a) did not move on PublishKey(a)")
	}
	if b.KeyVersion(GitChanged, "b") != b0 {
		t.Error("KeyVersion(b) moved on PublishKey(a)")
	}
	if b.Version(GitChanged) == total {
		t.Error("Version(kind) did not move on a keyed publish")
	}
}

// An unkeyed publish is a broadcast: a consumer scoped to any key must see it.
func TestUnkeyedPublishMovesEveryKey(t *testing.T) {
	b := New()
	b.PublishKey(GitChanged, "a")
	a0, never := b.KeyVersion(GitChanged, "a"), b.KeyVersion(GitChanged, "never-published")
	b.Publish(GitChanged)
	if b.KeyVersion(GitChanged, "a") == a0 || b.KeyVersion(GitChanged, "never-published") == never {
		t.Error("an unkeyed publish must move every key of the kind")
	}
}

func TestPublishKeyConcurrent(t *testing.T) {
	b := New()
	done := make(chan struct{})
	for _, k := range []string{"a", "b"} {
		go func() {
			for range 1000 {
				b.PublishKey(GitChanged, k)
			}
			done <- struct{}{}
		}()
	}
	for range 1000 {
		_ = b.KeyVersion(GitChanged, "a")
	}
	<-done
	<-done
	if got := b.KeyVersion(GitChanged, "a") + b.KeyVersion(GitChanged, "b"); got != 2000 {
		t.Errorf("keyed versions sum = %d, want 2000", got)
	}
	if got := b.KeySeq(GitChanged); got != 2000 {
		t.Errorf("KeySeq = %d after 2000 keyed publishes", got)
	}
}

// A keyed publish moves KeySeq and its own mark, not another key's, and an
// unkeyed publish moves BroadVersion and every key but no mark.
func TestKeySeqAndMarks(t *testing.T) {
	b := New()
	b.PublishKey(RBACChanged, "x")
	since, broad, x0 := b.KeySeq(RBACChanged), b.BroadVersion(RBACChanged), b.KeyMark(RBACChanged, "x")
	b.PublishKey(RBACChanged, "y")
	b.PublishKey(RBACChanged, "y")
	if b.KeySeq(RBACChanged) == since || b.KeyMark(RBACChanged, "y") <= since || b.KeyMark(RBACChanged, "x") != x0 {
		t.Errorf("seq %d, marks x %d y %d after a reading of %d", b.KeySeq(RBACChanged), b.KeyMark(RBACChanged, "x"), b.KeyMark(RBACChanged, "y"), since)
	}
	if b.BroadVersion(RBACChanged) != broad {
		t.Error("a keyed publish moved BroadVersion")
	}
	x := b.KeyVersion(RBACChanged, "x")
	b.Publish(RBACChanged)
	if b.BroadVersion(RBACChanged) == broad || b.KeyVersion(RBACChanged, "x") == x {
		t.Error("an unkeyed publish must move BroadVersion and every key")
	}
	if b.KeyMark(RBACChanged, "x") != x0 {
		t.Error("an unkeyed publish moved a mark")
	}
}

// A stamp of Version is never newer than the key levels a build reads after
// it: while a publish's key state is still being written (the lock held, as a
// long MovedSince scan holds it), Version has not moved, and once it has, the
// keys have too. A hub stamping a frame with Version therefore never records
// a level its build did not see.
func TestVersionNeverLeadsTheKeys(t *testing.T) {
	b := New()
	l := &b.keys[RBACChanged]
	l.mu.Lock()
	done := make(chan struct{})
	go func() {
		b.PublishKey(RBACChanged, "x")
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	if v := b.Version(RBACChanged); v != 0 {
		t.Errorf("Version moved to %d before the key state was written", v)
	}
	l.mu.Unlock()
	<-done
	if b.Version(RBACChanged) != 1 || b.KeySeq(RBACChanged) != 1 {
		t.Fatalf("after the publish: Version %d, KeySeq %d", b.Version(RBACChanged), b.KeySeq(RBACChanged))
	}
}

// Under concurrent publishes of both forms, every Version reading is covered
// by the key levels read after it.
func TestKeyLevelsCoverVersion(t *testing.T) {
	b := New()
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for i := range 20000 {
			if i%3 == 0 {
				b.Publish(RBACChanged)
			} else {
				b.PublishKey(RBACChanged, "x")
			}
		}
	}()
	for {
		select {
		case <-stop:
			return
		default:
		}
		v := b.Version(RBACChanged)
		if levels := b.BroadVersion(RBACChanged) + b.KeySeq(RBACChanged); levels < v {
			t.Fatalf("Version %d read ahead of the key levels %d", v, levels)
		}
	}
}

// A forgotten key reads as never published, and its next publish marks it
// above every mark it had: a namespace deleted, forgotten, recreated and
// moved once can never re-equal a stamp taken before.
func TestForgetNeverReequalsAMark(t *testing.T) {
	b := New()
	b.PublishKey(RBACChanged, "x")
	stamp := b.KeyMark(RBACChanged, "x")
	b.Forget(RBACChanged, "x")
	if b.KeyMark(RBACChanged, "x") != 0 {
		t.Fatal("a forgotten key still reads published")
	}
	b.PublishKey(RBACChanged, "x")
	if got := b.KeyMark(RBACChanged, "x"); got <= stamp {
		t.Fatalf("the recreated key's first move marks %d, at or below the old stamp %d", got, stamp)
	}
}
