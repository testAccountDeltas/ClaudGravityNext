// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale. Split out of
// snapshot.go purely to stay under the Part 4.1 file-size ceiling (the
// combined file was 428 lines) — the function body is unchanged from v1.
package editrepair

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// RecordSessionFileSnapshots replays the Read/Write/Edit tool calls already
// present in this request's own message history to reconstruct each file's
// last known state AS THE MODEL ITSELF SAW/PRODUCED IT.
//
// This is the actually-correct ground truth for RepairEditToolCall: when
// old_string fails to byte-match, it's because the model mis-transcribed
// whitespace/tabs/newlines from content it already read earlier in this
// same conversation -- not because the file is unreachable or "new". The
// conversation already carries that content over the wire to the gateway
// regardless of where the gateway runs, so this needs no local disk access
// at all (works for a remote-deployed gateway, unlike repairEditToolCallCore's
// os.ReadFile).
func RecordSessionFileSnapshots(sessionID string, bodyBytes []byte) {
	if sessionID == "" {
		return
	}
	messages := gjson.GetBytes(bodyBytes, "messages")
	if !messages.IsArray() {
		return
	}

	toolIDToName := make(map[string]string)
	toolIDToPath := make(map[string]string)
	toolIDIsPartial := make(map[string]bool)
	toolIDToOffset := make(map[string]int)
	toolIDHasOffset := make(map[string]bool)

	// A tool_use's own result (is_error:true on a failed old_string match)
	// arrives in a LATER message than the tool_use itself, so it has to be
	// collected in its own pass before the main loop below — which replays
	// Edit's old_string/new_string against the tracked snapshot the moment
	// it sees the tool_use, with no way yet to know whether that edit
	// actually succeeded on the real file. Applying it unconditionally
	// poisoned the snapshot with content that was never actually written,
	// then handed back to later repair attempts as if it were real.
	failedToolIDs := make(map[string]bool)
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		if msg.Get("role").String() != "user" {
			return true
		}
		content.ForEach(func(_, p gjson.Result) bool {
			if p.Get("type").String() == "tool_result" && p.Get("is_error").Bool() {
				if tid := p.Get("tool_use_id").String(); tid != "" {
					failedToolIDs[tid] = true
				}
			}
			return true
		})
		return true
	})

	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		role := msg.Get("role").String()
		content.ForEach(func(_, p gjson.Result) bool {
			// A Round 15 audit caught that neither branch below checked the
			// enclosing message's role — tool_use is only ever real coming
			// from "assistant" (a user message quoting/pasting an example
			// tool call in their own text is valid JSON but not a real
			// file operation), and tool_result is only ever real coming
			// from "user". Without this, a user message that happened to
			// contain a tool_use-shaped block got replayed as if the model
			// had actually performed that file operation, corrupting the
			// snapshot with content that was never written to disk.
			ptype := p.Get("type").String()
			if ptype == "tool_use" && role != "assistant" {
				return true
			}
			if ptype == "tool_result" && role != "user" {
				return true
			}
			switch ptype {
			case "tool_use":
				id := p.Get("id").String()
				name := strings.ToLower(p.Get("name").String())
				if id == "" {
					return true
				}
				toolIDToName[id] = name

				input := p.Get("input")
				// notebook_path added — an audit caught that RepairEditToolCall
				// (the function that actually USES these snapshots) already
				// recognized it, but this recording pass didn't, so a
				// NotebookEdit tool call's file was never tracked at all.
				path := firstNonEmpty(input.Get("file_path").String(), input.Get("path").String(), input.Get("file").String(), input.Get("target_file").String(), input.Get("notebook_path").String())
				if path == "" {
					return true
				}
				toolIDToPath[id] = path
				if name == "read" {
					if input.Get("offset").Exists() {
						toolIDToOffset[id] = int(input.Get("offset").Int())
						toolIDHasOffset[id] = true
						toolIDIsPartial[id] = true
					} else if input.Get("limit").Exists() {
						toolIDIsPartial[id] = true
					}
				}
				if failedToolIDs[id] {
					return true
				}

				switch name {
				case "write":
					// Write hands the full new file content directly in the
					// call itself -- no need to wait for the tool_result.
					if c := input.Get("content"); c.Exists() {
						contentStr := c.String()
						setSessionSnapshot(sessionID, path, contentStr, true)
					}
				case "edit", "update", "str_replace_editor", "notebookedit":
					// Replay same-turn edits against whatever snapshot we
					// already have, so a second Edit on a file touched
					// earlier in this very request compares against
					// up-to-date content instead of the stale initial Read.
					// old_text/new_text added — same gap as notebook_path
					// above: RepairEditToolCall already recognized these
					// (NotebookEdit's own param names), this recording pass
					// didn't. new_source added too (a Round 15 audit caught
					// it was still missing here) — NotebookEdit's actual
					// Claude Code schema names its replacement content
					// new_source, not new_string/new_str/new_text.
					oldStr := firstNonEmpty(input.Get("old_string").String(), input.Get("old_str").String(), input.Get("pattern").String(), input.Get("old_text").String())
					newStr := firstNonEmpty(input.Get("new_string").String(), input.Get("new_str").String(), input.Get("new_text").String(), input.Get("new_source").String())
					if oldStr != "" {
						// An audit caught this lookup only ever tried the
						// literal `path` as given — if an earlier Read in
						// this same session recorded its snapshot under an
						// ABSOLUTE path (Claude Code's own tools normally
						// use absolute paths) and this same-turn replay then
						// names the file with a RELATIVE path instead, the
						// lookup missed the existing snapshot entirely,
						// even though the write-back two lines below this
						// already resolves against session CWD. Try the
						// CWD-resolved candidate too, same as
						// RepairEditToolCall's own snapshot lookup does.
						lookupPath := path
						cur, ok := getSessionSnapshot(sessionID, lookupPath)
						if !ok && !isAbsPath(path) {
							if cwd := GetSessionCWD(sessionID); cwd != "" {
								if resolved, rok := getSessionSnapshot(sessionID, joinCWD(cwd, path)); rok {
									cur, ok, lookupPath = resolved, true, joinCWD(cwd, path)
								}
							}
						}
						// An audit caught this compared cur/oldStr with no
						// \r\n vs \n normalization — a snapshot stored with
						// LF (the common case) never matched an incoming
						// old_string using CRLF (a Windows client), or vice
						// versa, even when the actual text content was
						// identical, silently skipping the snapshot update
						// and leaving later same-turn edits comparing
						// against stale content.
						matchOldStr, matchNewStr := oldStr, newStr
						matchCur := cur
						normalizedCRLF := false
						if ok && !strings.Contains(matchCur, matchOldStr) {
							normCur := strings.ReplaceAll(matchCur, "\r\n", "\n")
							normOld := strings.ReplaceAll(matchOldStr, "\r\n", "\n")
							if strings.Contains(normCur, normOld) {
								matchCur = normCur
								matchOldStr = normOld
								matchNewStr = strings.ReplaceAll(matchNewStr, "\r\n", "\n")
								normalizedCRLF = strings.Contains(cur, "\r\n")
							}
						}
						if ok && strings.Contains(matchCur, matchOldStr) {
							n := 1
							if input.Get("replace_all").Bool() {
								n = -1
							}
							updated := strings.Replace(matchCur, matchOldStr, matchNewStr, n)
							// An audit caught that this stored the LF-
							// normalized `updated` into the snapshot
							// whenever the CRLF/LF mismatch branch above
							// fired, discarding the real file's CRLF line
							// endings. The NEXT turn's own old_string gets
							// built from this (now LF) snapshot and compared
							// against the real on-disk file, which still has
							// CRLF — "old_string must match file exactly"
							// fails outright. Restore CRLF before storing,
							// mirroring the normalization above exactly
							// (matchCur/matchNewStr had every \r\n replaced
							// with \n, so every remaining \n in `updated` is
							// safe to turn back into \r\n).
							if normalizedCRLF {
								updated = strings.ReplaceAll(updated, "\n", "\r\n")
							}
							// Preserves whatever fullness the snapshot being
							// replayed-against already had — editing text
							// within a partial slice doesn't make it complete.
							wasFull := isSessionSnapshotFull(sessionID, lookupPath)
							setSessionSnapshot(sessionID, path, updated, wasFull)
						}
					}
				}

			case "tool_result":
				// Do not record snapshot if the tool call failed (e.g. file not found)
				if p.Get("is_error").Bool() || failedToolIDs[p.Get("tool_use_id").String()] {
					return true
				}
				toolUseID := p.Get("tool_use_id").String()
				if toolIDToName[toolUseID] != "read" {
					return true
				}
				path := toolIDToPath[toolUseID]
				if path == "" {
					return true
				}

				var resText string
				c := p.Get("content")
				if c.IsArray() {
					var sb strings.Builder
					c.ForEach(func(_, item gjson.Result) bool {
						if item.Type == gjson.String {
							sb.WriteString(item.String())
						} else if item.Get("type").String() == "text" {
							sb.WriteString(item.Get("text").String())
						}
						return true
					})
					resText = sb.String()
				} else if c.Exists() {
					resText = c.String()
				}
				if resText != "" {
					cleanContent := stripCatNPrefix(resText)
					isPartial := toolIDIsPartial[toolUseID]
					hasOffset := toolIDHasOffset[toolUseID]
					offsetVal := toolIDToOffset[toolUseID]

					if !hasOffset {
						cleanRes := strings.TrimRight(resText, "\r\n")
						firstLine := cleanRes
						if idx := strings.IndexByte(cleanRes, '\n'); idx != -1 {
							firstLine = cleanRes[:idx]
						}
						if num, _, ok := extractCatNLine(firstLine); ok && num > 1 {
							hasOffset = true
							offsetVal = num
							isPartial = true
						}
					}
					if !isPartial && strings.Count(cleanContent, "\n") >= 2000 {
						isPartial = true
					}
					lookupPath := path
					existing, hasExisting := getSessionSnapshot(sessionID, lookupPath)
					if !hasExisting && !isAbsPath(lookupPath) {
						if cwd := GetSessionCWD(sessionID); cwd != "" {
							cand := joinCWD(cwd, lookupPath)
							if ex, ok := getSessionSnapshot(sessionID, cand); ok {
								existing = ex
								hasExisting = true
								lookupPath = cand
							}
						}
					}
					if hasExisting && (isPartial || hasOffset) {
						cleanContent = mergeSnapshotWithSlice(existing, resText, offsetVal)
					} else if !hasExisting && (isPartial || hasOffset) && offsetVal > 1 {
						cleanContent = mergeSnapshotWithSlice("", resText, offsetVal)
					}
					wasFull := hasExisting && isSessionSnapshotFull(sessionID, lookupPath)
					isFull := (!isPartial && !hasOffset) || wasFull
					setSessionSnapshot(sessionID, path, cleanContent, isFull)
				}
			}
			return true
		})
		return true
	})
}

func extractCatNLine(line string) (int, string, bool) {
	line = strings.TrimRight(line, "\r")
	loc := catNLinePrefix.FindStringIndex(line)
	if loc == nil {
		return 0, line, false
	}
	prefix := strings.TrimSpace(line[:loc[1]-1])
	num, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, line[loc[1]:], false
	}
	return num, line[loc[1]:], true
}

func mergeSnapshotWithSlice(existing string, resText string, explicitOffset ...int) string {
	hasCRLF := strings.Contains(existing, "\r\n") || strings.Contains(resText, "\r\n")
	resText = strings.ReplaceAll(resText, "\r\n", "\n")
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	cleanRes := strings.TrimRight(resText, "\n")
	rawLines := strings.Split(cleanRes, "\n")
	if len(rawLines) == 0 {
		if hasCRLF {
			return strings.ReplaceAll(existing, "\n", "\r\n")
		}
		return existing
	}
	firstNum := -1
	firstNumIdx := -1
	for idx, l := range rawLines {
		if num, _, ok := extractCatNLine(l); ok && num > 0 {
			firstNum = num
			firstNumIdx = idx
			break
		}
	}

	var startIdx int
	if firstNum > 0 {
		startIdx = (firstNum - 1) - firstNumIdx
		if startIdx < 0 {
			startIdx = 0
		}
	} else if len(explicitOffset) > 0 && explicitOffset[0] > 0 {
		off := explicitOffset[0]
		startIdx = off - 1
		if startIdx < 0 {
			startIdx = 0
		}
	} else {
		return stripCatNPrefix(resText)
	}

	cleanNewLines := make([]string, len(rawLines))
	for i, l := range rawLines {
		if _, content, ok := extractCatNLine(l); ok {
			cleanNewLines[i] = content
		} else {
			cleanNewLines[i] = l
		}
	}

	var existingLines []string
	if existing != "" {
		existingLines = strings.Split(existing, "\n")
	}
	if startIdx > 50000 {
		return existing
	}
	for len(existingLines) < startIdx {
		existingLines = append(existingLines, "")
	}
	for i, nl := range cleanNewLines {
		targetIdx := startIdx + i
		if targetIdx < len(existingLines) {
			existingLines[targetIdx] = nl
		} else {
			existingLines = append(existingLines, nl)
		}
	}

	res := strings.Join(existingLines, "\n")
	if hasCRLF {
		res = strings.ReplaceAll(res, "\n", "\r\n")
	}
	return res
}
