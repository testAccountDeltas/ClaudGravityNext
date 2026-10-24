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
	if wd.ttftTimeout != 180*time.Second || wd.chunkTimeout != StreamingChunkTimeout ||
		wd.maxDuration != 30*time.Minute {
		t.Fatalf("defaults = ttft %v chunk %v max %v", wd.ttftTimeout, wd.chunkTimeout, wd.maxDuration)
	}
	if StreamingChunkTimeout != 180*time.Second {
		t.Fatalf("StreamingChunkTimeout = %v", StreamingChunkTimeout)
	}
	explicit := startIdleWatchdog(context.Background(), 2, 7*time.Second, 9*time.Second, 11*time.Minute)
	defer explicit.Stop()
	if explicit.ttftTimeout != 7*time.Second || explicit.chunkTimeout != 9*time.Second || explicit.maxDuration != 11*time.Minute {
		t.Fatalf("explicit values must be kept, got %v %v %v", explicit.ttftTimeout, explicit.chunkTimeout, explicit.maxDuration)
	}
}

func TestIdleWatchdog_PhaseSwitchesFromTTFTToChunkOnFirstTouch(t *testing.T) {
	wd := startIdleWatchdog(context.Background(), 1, 30*time.Second, 90*time.Second, time.Hour)
	defer wd.Stop()
	if lim, phase := wd.currentLimit(); lim != 30*time.Second || phase != "TTFT" {
		t.Fatalf("before any bytes: %v %s", lim, phase)
	}
	wd.Touch()
	if lim, phase := wd.currentLimit(); lim != 90*time.Second || phase != "inter-chunk" {
		t.Fatalf("after the first bytes: %v %s", lim, phase)
	}
}

func TestIdleWatchdog_TTFTLimitFiresBeforeFirstByte_ChunkLimitAfter(t *testing.T) {
	old := watchdogTick
	watchdogTick = time.Millisecond
	t.Cleanup(func() { watchdogTick = old })

	// Never touched: the short TTFT limit applies and fires.
	cold := startIdleWatchdog(context.Background(), 1, 20*time.Millisecond, time.Hour, time.Hour)
	defer cold.Stop()
	select {
	case <-cold.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("TTFT limit never fired")
	}

	// Touched once: the (much longer) chunk limit applies, so the short TTFT
	// limit must NOT fire; the watchdog is still alive after several ticks.
	warm := startIdleWatchdog(context.Background(), 2, 20*time.Millisecond, time.Hour, time.Hour)
	defer warm.Stop()
	warm.Touch()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-warm.ctx.Done():
			t.Fatal("TTFT limit applied after the first byte arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
