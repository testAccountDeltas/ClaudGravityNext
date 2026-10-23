// Part of the stream package — see events.go for the package doc
// comment. Split out of fsm.go purely to stay under the 400-line ceiling:
// the stop-sequence hold-back flush and the text-delta emitter.
package stream

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/sanitize"
)

// FlushTextHoldBuffer emits any held-back text accumulated by the stop-sequence
// buffer. If stripSeq is non-empty and the held text ends in stripSeq, that suffix
// is stripped before emission so the client never sees the stop sequence tokens.
func (f *FSM) FlushTextHoldBuffer(stripSeq string) error {
	if f.textHoldBuffer == "" {
		return nil
	}
	text := f.textHoldBuffer
	f.textHoldBuffer = ""
	if stripSeq != "" {
		// Everything from the matched stop sequence on is dropped: the
		// client must never see the sequence (or anything after it).
		if i := strings.Index(text, stripSeq); i >= 0 {
			text = text[:i]
		}
	}
	text = sanitize.StripSyntheticXMLTags(text)
	if text == "" {
		return nil
	}
	return f.emitTextDelta(text)
}

func (f *FSM) emitTextDelta(text string) error {
	if text == "" || f.capReached {
		return nil
	}
	text, f.capReached = f.fitToCap(text)
	if text == "" {
		return nil
	}
	if f.currentBlock != BlockText {
		// First visible content: from here on the turn is committed to the
		// client (held message_start/thinking events are flushed first).
		if err := f.writer.Release(); err != nil {
			return err
		}
		if err := f.closeCurrentBlock(); err != nil {
			return err
		}
		f.currentIndex++
		f.currentBlock = BlockText

		startPayload := map[string]interface{}{
			"type":  "content_block_start",
			"index": f.currentIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}
		b, _ := json.Marshal(startPayload)
		if err := f.writer.EmitEvent("content_block_start", string(b)); err != nil {
			return err
		}
	}

	deltaPayload := map[string]interface{}{
		"type":  "content_block_delta",
		"index": f.currentIndex,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": text,
		},
	}
	f.textChars += len(text)
	b, _ := json.Marshal(deltaPayload)
	return f.writer.EmitEvent("content_block_delta", string(b))
}
