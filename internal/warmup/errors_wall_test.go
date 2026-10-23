package warmup

import "testing"

func TestSessionTokenWall_OnlyBumpsWhenEstimateClearlyUnderWindow(t *testing.T) {
	wall := `HTTP 400: {"error":{"message":"The input token count exceeds the maximum number of tokens allowed (1048576)."}}`
	if !IsSessionTokenWallErrorForEstimate(400, wall, 5_000) {
		t.Fatal("tiny prompt + wall error must be treated as the session wall")
	}
	if !IsSessionTokenWallErrorForEstimate(400, wall, 899_999) {
		t.Fatal("just under threshold must still bump")
	}
	for _, est := range []int{900_000, 1_200_000} {
		if IsSessionTokenWallErrorForEstimate(400, wall, est) {
			t.Fatalf("estimate %d is a genuinely oversized prompt, must fail fast", est)
		}
	}
	if IsSessionTokenWallErrorForEstimate(429, wall, 10) {
		t.Fatal("non-400 is never the wall")
	}
}
