package main

import (
	"errors"
	"testing"
	"time"
)

func TestRetryUntilOK_BacksOffAndStopsOnSuccess(t *testing.T) {
	var waits []time.Duration
	calls := 0
	tries := retryUntilOK(func() error {
		calls++
		if calls < 5 {
			return errors.New("boom")
		}
		return nil
	}, time.Second, 6*time.Second, func(d time.Duration) { waits = append(waits, d) })
	if tries != 5 || calls != 5 {
		t.Fatalf("tries=%d calls=%d", tries, calls)
	}
	want := []time.Duration{1, 2, 4, 6, 6}
	for i, w := range want {
		if waits[i] != w*time.Second {
			t.Fatalf("waits = %v", waits)
		}
	}
}
