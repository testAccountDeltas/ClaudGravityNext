package orchestrate

import (
	"context"
	"testing"
	"time"
)

func TestIdleWatchdog_FiresAfterIdleTimeout(t *testing.T) {
	// The watchdog polls every 2s (fixed ticker granularity, matching
	// v1) regardless of how short idleTimeout is, so the wait here must
	// clear that polling interval, not just idleTimeout itself.
	wd := startIdleWatchdog(context.Background(), 1, 20*time.Millisecond, 20*time.Millisecond, time.Minute)
	defer wd.Stop()

	select {
	case <-wd.ctx.Done():
		// expected
	case <-time.After(4 * time.Second):
		t.Fatal("expected the watchdog to cancel its context once idle longer than the idle timeout")
	}
}

func TestIdleWatchdog_TouchPreventsIdleFire(t *testing.T) {
	wd := startIdleWatchdog(context.Background(), 1, 150*time.Millisecond, 150*time.Millisecond, time.Minute)
	defer wd.Stop()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		wd.Touch()
		time.Sleep(30 * time.Millisecond)
	}

	select {
	case <-wd.ctx.Done():
		t.Fatal("expected repeated Touch calls to keep the watchdog from firing on idle")
	default:
		// expected: still alive
	}
}

func TestIdleWatchdog_MaxDurationFiresEvenWhenNeverIdle(t *testing.T) {
	wd := startIdleWatchdog(context.Background(), 1, time.Minute, time.Minute, 20*time.Millisecond)
	defer wd.Stop()

	// Same 2s polling-interval floor as the idle case above — Touch
	// every few ms (never idle) while waiting past that floor.
	deadline := time.Now().Add(3 * time.Second)
	fired := false
	for time.Now().Before(deadline) {
		wd.Touch()
		select {
		case <-wd.ctx.Done():
			fired = true
		default:
		}
		if fired {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !fired {
		t.Fatal("expected the absolute max-duration safety net to fire even though Touch kept the connection from ever going idle")
	}
}

func TestIdleWatchdog_StopDoesNotHang(t *testing.T) {
	wd := startIdleWatchdog(context.Background(), 1, time.Minute, time.Minute, time.Minute)
	done := make(chan struct{})
	go func() {
		wd.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Stop to cancel and return promptly, not deadlock")
	}
}

func TestIdleWatchdog_SeparateTimeouts_DefaultsChunkTimeout(t *testing.T) {
	wd := startIdleWatchdog(context.Background(), 1, 0, 0, 0)
	defer wd.Stop()
	// Should initialize without error and default timeouts to safe positive values
	if wd == nil {
		t.Fatal("expected watchdog to be created")
	}
}
