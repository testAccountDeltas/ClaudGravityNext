package main

import (
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/logx"
)

// retryModelCatalog keeps asking for the model catalog after a failed start-up
// fetch. Without it one transient failure at launch left the catalog empty
// until the next restart, and every limit taken from it (output ceiling,
// thinking floor, context window) silently did not apply.
func retryModelCatalog() {
	retryUntilOK(func() error {
		_, err := accounts.Manager().FetchAvailableModels(true)
		return err
	}, 30*time.Second, 10*time.Minute, time.Sleep)
}

// retryUntilOK calls fetch until it succeeds, doubling the wait from first up to max.
func retryUntilOK(fetch func() error, first, max time.Duration, sleep func(time.Duration)) int {
	delay, tries := first, 0
	for {
		sleep(delay)
		tries++
		err := fetch()
		if err == nil {
			logx.LogGateway("[ClaudGravityNext] Model catalog loaded after %d retry attempt(s)", tries)
			return tries
		}
		logx.LogGateway("[ClaudGravityNext] Model catalog fetch retry %d failed: %v", tries, err)
		if delay *= 2; delay > max {
			delay = max
		}
	}
}
