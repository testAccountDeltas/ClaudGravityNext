// Part of cmd/shadowcompare — see main.go's package doc comment. The
// actual diff/redact logic now lives in internal/jsondiff (extracted so
// pkg/speccheck/rules' TranslateStructuralParity rule can reuse it
// without duplicating this file); these are thin aliases kept so the
// rest of this package's call sites don't change.
package main

import "claudgravity/internal/jsondiff"

func diffJSON(path string, a, b interface{}, out *[]string) {
	jsondiff.Diff(path, a, b, out)
}
