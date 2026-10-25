package cacheusage

import "testing"

func TestCov_Clamp(t *testing.T) {
	for in, want := range map[float64]float64{-0.5: 0, 0: 0, 0.25: 0.25, 1: 1, 7: 1} {
		if got := clamp(in); got != want {
			t.Errorf("clamp(%v)=%v want %v", in, got, want)
		}
	}
}
