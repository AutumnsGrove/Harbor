package enrich

import (
	"context"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// Images are the one save where the bytes are the content: there is no page to
// read, so the model is the reader. One call answers everything the library
// needs from a picture — what it is, what it says, what it shows, and what
// colours it holds — because a second call would pay the image tokens twice for
// answers that share a context (open-questions H1).

// VisionInput is what an image save can tell the model: the bytes, their type,
// the filename as a weak signal, and the tag vocabulary so a new tag is not a
// near-duplicate of an existing one.
type VisionInput struct {
	// Image is the original bytes. It is never the thumbnail: OCR needs the
	// resolution, and the provider sees the same file the library keeps.
	Image []byte
	// MimeType is what the bytes actually are, sniffed, not what a server said.
	MimeType string
	// Filename is a hint only: "Screenshot 2026-11-27 at 09.14.png" is worth a
	// little, and "IMG_1861.JPG" is worth nothing.
	Filename   string
	Vocabulary []string
}

// Vision is the answer. Every field is optional in the sense that a model may
// return an empty one; the caller decides what an empty field means. Kind is the
// only one the pipeline coerces, because a kind outside the twelve cannot be
// stored.
type Vision struct {
	Kind        model.Kind
	Tags        []string
	OCRText     string
	Description string
	// Colors are hex strings the model saw, most prominent first. They are a
	// hint for the card's placeholder, never the card.
	Colors []string
}

// Visioner describes one image. A nil Visioner is a valid state — the pipeline
// then stores the image and badges it — so a deployment without a vision model
// loses descriptions rather than losing saves.
type Visioner interface {
	Describe(ctx context.Context, input VisionInput) (Vision, error)
}
