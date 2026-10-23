package pathkey

import "testing"

func TestCanonical_SlashAndDriveLetterNormalization(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"src/app.ts", "src/app.ts"},
		{`src\app.ts`, "src/app.ts"},
		{`H:\project\src\app.ts`, "h:/project/src/app.ts"},
		{`h:\project\src\app.ts`, "h:/project/src/app.ts"},
		{"  src/app.ts  ", "src/app.ts"},
	}
	for _, c := range cases {
		if got := Canonical(c.in); got != c.want {
			t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonical_SameKeyAcrossVariants(t *testing.T) {
	a := Canonical(`H:\project\src\app.ts`)
	b := Canonical("h:/project/src/app.ts")
	if a != b {
		t.Fatalf("expected same canonical key, got %q vs %q", a, b)
	}
}
