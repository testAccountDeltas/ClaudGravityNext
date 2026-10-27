package tiering

import "testing"

func TestFamilyOverride_SonnetToNative(t *testing.T) {
	t.Cleanup(func() { SetFamilyOverrides(nil) })
	req := func() map[string]interface{} { return map[string]interface{}{"model": "claude-sonnet-4-5"} }
	if m, _ := ResolveTieringAndEffort(req(), "main", ""); m != "gemini-3.8-flash-medium" {
		t.Fatalf("default sonnet must stay on Gemini, got %s", m)
	}
	if sk := SetFamilyOverrides(map[string]string{"sonnet": "claude-sonnet-4-6", "opus": "nonsense", "x": "claude-sonnet-4-6"}); len(sk) != 2 {
		t.Fatalf("expected 2 skipped entries, got %v", sk)
	}
	if m, _ := ResolveTieringAndEffort(req(), "main", ""); m != "claude-sonnet-4-6" {
		t.Fatalf("sonnet override not applied, got %s", m)
	}
	opus := map[string]interface{}{"model": "claude-opus-4-5"}
	if m, _ := ResolveTieringAndEffort(opus, "main", ""); m != "gemini-3.8-flash-tiered" {
		t.Fatalf("opus must stay on Gemini, got %s", m)
	}
}
