// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale. This file holds
// RepairEditToolCall and its core implementation.
package editrepair

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/logx"
)

var targetFileKeys = []string{"file_path", "path", "file", "notebook_path", "target_file"}

// RepairEditToolCall checks if a tool call is an Edit/str_replace_editor/Update call,
// and if old_string does not match byte-for-byte in the file on disk, repairs it using fuzzy whitespace matching.
// RepairEditToolCall repairs a fuzzy-matched old_string/new_string against
// the real file, then normalizes whatever alias parameter names the model
// used (old_str/new_str/path/notebook_path/target_file/pattern/old_text/
// new_text) to the exact names Claude Code's own Edit tool schema requires
// (file_path/old_string/new_string). An audit caught that the repair logic
// below only ever rewrote the VALUE under whichever key the model used,
// never the key itself — a model that called Edit with {"path":...,
// "old_str":...,"new_str":...} got back the same alias names in
// tool_use.input, which Claude Code's Zod-schema-validated tool dispatcher
// rejects outright, since it requires file_path/old_string/new_string
// exactly.
func RepairEditToolCall(toolName string, argsRaw string, sessionID string) (string, bool) {
	repaired, changed := repairEditToolCallCore(toolName, argsRaw, sessionID)
	normalized, renamed := normalizeEditToolCallKeys(toolName, repaired)
	if renamed {
		return normalized, true
	}
	return repaired, changed
}

// normalizeEditToolCallKeys renames any alias key present to its canonical
// Claude Code Edit-tool name, deleting the alias. Safe to call even when no
// repair happened — it only touches key names, never values.
func normalizeEditToolCallKeys(toolName string, argsRaw string) (string, bool) {
	// NotebookEdit's real Claude Code schema uses notebook_path/new_source,
	// not file_path/old_string/new_string — it's excluded here (unlike
	// repairEditToolCallCore's broader alias-reading above, which is safe
	// either way since it only reads a value, never renames a key) because
	// renaming its genuinely-correct "notebook_path" to "file_path" would
	// break it, not fix it.
	nameLower := strings.ToLower(toolName)
	// An audit caught that renaming keys for "str_replace_editor" here
	// was actively wrong: that's Anthropic's own OFFICIAL built-in
	// text-editor tool name (the convention a real Anthropic API client
	// — not Claude Code, which has a completely different tool of its
	// own called "Edit" — uses when declaring text_editor_* without a
	// custom name). Its REAL canonical argument names are command/path/
	// old_str/new_str (builtinAnthropicToolSchema declares exactly
	// these), and a genuine Anthropic SDK client's own schema validation
	// expects path/old_str/new_str back unchanged — renaming them to
	// file_path/old_string/new_string broke a client using the tool
	// exactly as officially specified. Only Claude Code's own "Edit" (and
	// the generic MCP "update" convention) actually need this rename;
	// str_replace_editor's args still benefit from the broader VALUE
	// repair elsewhere in this file (whitespace/indentation fixing),
	// which reads either naming convention without ever renaming a key.
	isEditLike := nameLower == "edit" || nameLower == "update"
	// A follow-up audit caught that Write and Read — real Claude Code
	// tools needing the exact same file_path key, just with no old_string/
	// new_string concept at all — got NO alias normalization whatsoever,
	// the same "path" confusion already fixed for Edit. (Read has no
	// "content" input parameter to alias at all — the audit's claimed
	// "text -> content for Read" doesn't correspond to a real field —
	// so only file_path is normalized for it, same as Write.)
	isWriteOrRead := nameLower == "write" || nameLower == "read"
	if !isEditLike && !isWriteOrRead {
		return argsRaw, false
	}
	parsed := gjson.Parse(argsRaw)
	if !parsed.IsObject() {
		return argsRaw, false
	}
	renamed := false
	rename := func(canonical string, aliases []string) {
		if parsed.Get(canonical).Exists() && strings.TrimSpace(parsed.Get(canonical).String()) != "" {
			return
		}
		for _, alias := range aliases {
			if val := parsed.Get(alias); val.Exists() && strings.TrimSpace(val.String()) != "" {
				argsRaw, _ = sjson.SetRaw(argsRaw, canonical, val.Raw)
				argsRaw, _ = sjson.Delete(argsRaw, alias)
				renamed = true
				return
			}
		}
	}
	rename("file_path", []string{"path", "file", "notebook_path", "target_file"})
	if isEditLike {
		rename("old_string", []string{"old_str", "old_text", "pattern"})
		rename("new_string", []string{"new_str", "new_text"})
	}
	if nameLower == "write" {
		// Write's real "content" field is plausibly confused with "text",
		// the same way models confuse "content"/"text" for tool_result
		// bodies elsewhere in this gateway's own translation layer.
		rename("content", []string{"text"})
	}
	return argsRaw, renamed
}

func repairEditToolCallCore(toolName string, argsRaw string, sessionID string) (string, bool) {
	nameLower := strings.ToLower(toolName)
	if nameLower != "edit" && nameLower != "str_replace_editor" && nameLower != "str_replace_based_edit_tool" && nameLower != "text_editor" && nameLower != "update" && nameLower != "notebookedit" {
		return argsRaw, false
	}

	parsed := gjson.Parse(argsRaw)
	if !parsed.IsObject() {
		return argsRaw, false
	}

	// 1. Locate file path
	targetPath := ""
	for _, k := range targetFileKeys {
		if val := parsed.Get(k).String(); val != "" {
			targetPath = val
			break
		}
	}
	if targetPath == "" {
		return argsRaw, false
	}

	// 2. Locate old_string
	oldStrKey := ""
	oldStr := ""
	for _, k := range []string{"old_string", "old_str", "old_text", "pattern"} {
		if val := parsed.Get(k); val.Exists() {
			oldStrKey = k
			oldStr = val.String()
			break
		}
	}
	if oldStrKey == "" || oldStr == "" {
		return argsRaw, false
	}

	// Locate new_string's key too (same naming pairs as old_string, minus
	// "pattern" which has no replacement-text counterpart), so cat -n cleanup,
	// line-ending normalization, and indent repair can operate on it.
	newStrKey := ""
	newStr := ""
	// GAP-ED-02: "new_source" is NotebookEdit's real key for the new cell
	// content — never checked here, so a NotebookEdit call's replacement
	// text was invisible to this repair path.
	for _, k := range []string{"new_string", "new_str", "new_text", "new_source"} {
		if val := parsed.Get(k); val.Exists() {
			newStrKey = k
			newStr = val.String()
			break
		}
	}
	// 3. Resolve path against filesystem and session CWD
	cwdCandidate := ""
	resolvedPath := targetPath
	if !isAbsPath(resolvedPath) {
		cwd := GetSessionCWD(sessionID)
		if cwd != "" {
			cwdCandidate = joinCWD(cwd, resolvedPath)
			if _, err := os.Stat(cwdCandidate); err == nil {
				resolvedPath = cwdCandidate
			}
		}
	}

	// Sandbox: this function reads arbitrary client-supplied paths off the
	// GATEWAY's own disk to compare against old_string. Without a boundary
	// check, any tenant with a valid API key could make the gateway read
	// any file it has OS permission to open (/etc/shadow-equivalent,
	// other tenants' config, ...) just by naming it in a fake Edit call.
	// Require the resolved path to live inside the session's own recorded
	// working directory; if that's unknown, refuse rather than guess.
	//
	// This check guards ONLY the disk-read path below. The snapshot
	// fallback (getSessionSnapshot) touches no disk and exposes nothing the
	// client didn't already send itself in this very request, so it's safe
	// to use even when the sandbox check fails or the gateway has no
	// filesystem access to the client's files at all (remote deployment).
	var fileContent string
	if isWithinSessionCWD(resolvedPath, sessionID) {
		if fi, err := os.Stat(resolvedPath); err == nil && fi.Size() <= 2*1024*1024 {
			if data, err := os.ReadFile(resolvedPath); err == nil {
				fileContent = string(data)
			}
		}
	}
	if fileContent == "" {
		snap, ok := getSessionSnapshot(sessionID, targetPath)
		if !ok {
			snap, ok = getSessionSnapshot(sessionID, resolvedPath)
		}
		if !ok && cwdCandidate != "" {
			snap, ok = getSessionSnapshot(sessionID, cwdCandidate)
		}
		if !ok {
			return argsRaw, false
		}
		fileContent = snap
	}

	cleanNewStr := func() bool {
		if newStrKey != "" && newStr != "" {
			if cleanedNew := stripCatNPrefix(newStr); cleanedNew != newStr {
				newStr = cleanedNew
				if withCleanNew, err := sjson.Set(argsRaw, newStrKey, cleanedNew); err == nil {
					argsRaw = withCleanNew
				}
				return true
			}
		}
		return false
	}

	// Direct byte-for-byte match already exists!
	if strings.Contains(fileContent, oldStr) {
		changed := false
		if isCatNDump(newStr) && cleanNewStr() {
			changed = true
		}
		if newStrKey != "" {
			fixed, ok1 := matchIndentStyle(fileContent, newStr)
			fixed, ok2 := matchLineEndings(fileContent, fixed)
			if ok1 || ok2 {
				if withNew, err := sjson.Set(argsRaw, newStrKey, fixed); err == nil {
					argsRaw, changed = withNew, true
				}
			}
		}
		return argsRaw, changed
	}

	// An audit caught that stripCatNPrefix (added so a model copying its
	// own prior Read-tool output, including the "cat -n"-style "   42\t"
	// line-number prefix Read's dump carries, could still match) used to
	// run UNCONDITIONALLY before this exact-match check even ran — its
	// regex (`^\s*\d+\t`, digits then a literal tab) has no way to tell
	// a genuine Read-dump artifact apart from a real data line that just
	// happens to start the same way (a TSV row "1\tactive\ttrue", a
	// Makefile target index, ...). Mutating old_string BEFORE checking
	// whether the ORIGINAL already matched meant a real data line's
	// exact match was destroyed before ever being tried. Only attempt
	// the strip now, as a fallback, after the original has already
	// failed to match on its own.
	catNCleaned := false
	if cleaned := stripCatNPrefix(oldStr); cleaned != oldStr {
		if strings.Contains(fileContent, cleaned) {
			oldStr = cleaned
			catNCleaned = true
			if withClean, err := sjson.Set(argsRaw, oldStrKey, oldStr); err == nil {
				argsRaw = withClean
			}
			cleanNewStr()
			return argsRaw, true
		}
		// Didn't produce an exact match either, but may still help the
		// fuzzy matcher below (e.g. combined with tab/space repair) —
		// keep the cleaned version for the remainder of this function,
		// same as before.
		oldStr = cleaned
		catNCleaned = true
		if withClean, err := sjson.Set(argsRaw, oldStrKey, oldStr); err == nil {
			argsRaw = withClean
		}
		cleanNewStr()
	} else {
		if isCatNDump(newStr) {
			cleanNewStr()
		}
	}

	// 4. Try newline normalization (\r\n vs \n)
	// An audit caught that gating the LF candidate on an else-branch of
	// hasCRLF (true the moment the file contains a SINGLE \r\n anywhere)
	// made it unreachable for a file that's mostly LF with one stray
	// CRLF elsewhere — the target section needing plain LF-normalization
	// never got tried at all. Try both candidates; a file can legitimately
	// contain both line-ending styles in different regions.
	hasCRLF := strings.Contains(fileContent, "\r\n")
	normalizedLF := strings.ReplaceAll(oldStr, "\r\n", "\n")
	if hasCRLF {
		crlfCandidate := strings.ReplaceAll(normalizedLF, "\n", "\r\n")
		if strings.Contains(fileContent, crlfCandidate) {
			repaired, err := sjson.Set(argsRaw, oldStrKey, crlfCandidate)
			if err == nil {
				// Without this, old_string now matches the file's real CRLF
				// line endings but new_string (still LF, as the model wrote
				// it) gets substituted in verbatim, leaving the edited span
				// with LF endings inside an otherwise-CRLF file.
				if newStrKey != "" {
					newCRLFCandidate := strings.ReplaceAll(strings.ReplaceAll(newStr, "\r\n", "\n"), "\n", "\r\n")
					if newCRLFCandidate != newStr {
						if withNew, err2 := sjson.Set(repaired, newStrKey, newCRLFCandidate); err2 == nil {
							repaired = withNew
						}
					}
				}
				logx.LogGateway("[Auto-Repair Edit] Fixed CRLF newlines for %s (len: %d)", filepath.Base(resolvedPath), len(crlfCandidate))
				return repaired, true
			}
		}
	}
	if strings.Contains(fileContent, normalizedLF) {
		repaired, err := sjson.Set(argsRaw, oldStrKey, normalizedLF)
		if err == nil {
			if newStrKey != "" {
				normalizedNewLF := strings.ReplaceAll(newStr, "\r\n", "\n")
				if normalizedNewLF != newStr {
					if withNew, err2 := sjson.Set(repaired, newStrKey, normalizedNewLF); err2 == nil {
						repaired = withNew
					}
				}
			}
			logx.LogGateway("[Auto-Repair Edit] Fixed LF newlines for %s (len: %d)", filepath.Base(resolvedPath), len(normalizedLF))
			return repaired, true
		}
	}

	// 5. Advanced Whitespace-Insensitive & Tab Matching (Line-by-line Sliding Window)
	repairedExact, lineIndents, fileLine0Indent, ok := findFuzzyLineMatch(fileContent, oldStr)
	if ok && repairedExact != "" {
		repaired, err := sjson.Set(argsRaw, oldStrKey, repairedExact)
		if err == nil {
			// Ensure new_string matches CRLF/LF line endings of file
			if newStrKey != "" {
				effectiveNewStr := newStr
				// A Round 15 audit caught that this reused the file-WIDE
				// hasCRLF from step 4 above — a 10,000-line LF file with a
				// single stray \r\n somewhere unrelated (a comment, a
				// binary-ish string literal) made hasCRLF true for the
				// whole file, forcing CRLF onto new_string even though the
				// actual matched segment (repairedExact, with its own real
				// per-line endings from findFuzzyLineMatch) is plain LF.
				// Decide based on the matched segment itself, not the file.
				segmentHasCRLF := strings.Contains(repairedExact, "\r\n")
				if segmentHasCRLF {
					newCRLFCandidate := strings.ReplaceAll(strings.ReplaceAll(newStr, "\r\n", "\n"), "\n", "\r\n")
					if newCRLFCandidate != newStr {
						effectiveNewStr = newCRLFCandidate
					}
				} else {
					newLFCandidate := strings.ReplaceAll(newStr, "\r\n", "\n")
					if newLFCandidate != newStr {
						effectiveNewStr = newLFCandidate
					}
				}
				// Re-apply the indentation findFuzzyLineMatch detected as
				// missing or mismatched on any line, including line 0 when
				// old_string's own line 0 had SOME indentation of its own
				// that didn't match the file's real indentation (a
				// zero-indentation line 0 is handled earlier via
				// repairedExact's start-offset shift instead). When
				// new_string's line count differs from old_string's (a
				// line added/removed), reapplyLineIndents falls back to a
				// uniform best-effort indent for lines 1..N instead of a
				// precise 1:1 mapping.
				if styled, ok := matchIndentStyle(fileContent, effectiveNewStr); ok {
					effectiveNewStr = styled
				}
				if reindented, did := reapplyLineIndents(effectiveNewStr, lineIndents, fileLine0Indent, oldStr); did {
					effectiveNewStr = reindented
				}
				if effectiveNewStr != newStr {
					if withNew, err2 := sjson.Set(repaired, newStrKey, effectiveNewStr); err2 == nil {
						repaired = withNew
					}
				}
			}
			logx.LogGateway("[Auto-Repair Edit] Successfully fuzzy-repaired old_string for %s (%d -> %d bytes, tabs/spaces matched)",
				filepath.Base(resolvedPath), len(oldStr), len(repairedExact))
			return repaired, true
		}
	}

	return argsRaw, catNCleaned
}
