package anthropicerr

import "testing"

func TestCov_PromptTooLongMessage(t *testing.T) {
	cases := []struct {
		name, err  string
		est, limit int64
		want       string
	}{
		{"counts pattern", "The input token count (1500000) exceeds the maximum number of tokens allowed (1048576).", 1, 2, "prompt is too long: 1500000 tokens > 1048576 maximum"},
		{"bare numbers", "request has 2000000 tokens but 1000000 is the cap", 1, 2, "prompt is too long: 2000000 tokens > 1000000 maximum"},
		{"bare numbers not overflow ignored", "values 1000000 and 2000000", 50, 40, "prompt is too long: 50 tokens > 40 maximum"},
		{"fallback estimate", "no numbers", 300, 200, "prompt is too long: 300 tokens > 200 maximum"},
		{"estimate bumped past limit", "no numbers", 100, 200, "prompt is too long: 201 tokens > 200 maximum"},
		{"zero limit defaults", "", 0, 0, "prompt is too long: 1048577 tokens > 1048576 maximum"},
		{"counts with zero ignored", "token count (0) exceeds the maximum allowed (0)", 9, 5, "prompt is too long: 9 tokens > 5 maximum"},
	}
	for _, c := range cases {
		if got := PromptTooLongMessage(c.err, c.est, c.limit); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
