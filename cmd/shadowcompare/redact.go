// Part of cmd/shadowcompare — see main.go's package doc comment. The
// actual redaction logic now lives in internal/jsondiff (see diff.go's
// comment for why); this is a thin alias.
package main

import "claudgravity/internal/jsondiff"

func redactSkeleton(v interface{}) interface{} {
	return jsondiff.Redact(v)
}
