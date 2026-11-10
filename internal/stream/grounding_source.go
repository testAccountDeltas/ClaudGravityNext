// Part of the stream package — see events.go for the package doc
// comment. Split out of fsm.go purely to stay under the 400-line ceiling.
package stream

// GroundingSource is one Google Search result surfaced via Gemini's
// native grounding, appended as a footer once generation finishes.
type GroundingSource struct {
	Title string `json:"title"`
	URI   string `json:"uri"`
}
