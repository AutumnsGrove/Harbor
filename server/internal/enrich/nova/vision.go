package nova

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// Vision is one call per image, returning everything the library needs from it
// (open-questions H1): the kind, the tags, the text in it, a description, and
// the colours. A second call would pay for the image tokens twice to ask about
// the same pixels.
const (
	// VisionPromptVersion names the prompt file and is written to the item's
	// prompt_version, so a row says which prompt described it.
	VisionPromptVersion = "vision.v1"

	// visionModel accepts image input (open-questions H1). It is the model the
	// text enrichment already uses, which is why a picture and a page share a
	// bill and a vocabulary.
	visionModel = "deepseek/deepseek-v4.1-flash-1017"

	// visionMaxTokens is a budget for OCR as much as for prose: a screenshot of a
	// long article can carry thousands of characters of text, and a summary-sized
	// budget would truncate the transcription rather than the description.
	visionMaxTokens = 3000

	// visionMaxEncodedBytes is the ceiling for one request body, and the body
	// carries the image base64-encoded. The store's 25 MB is about disk; this is
	// about what a single request can be.
	//
	// It is deliberately the *encoded* number: base64 is a third larger than the
	// bytes it carries, so a raw ceiling of the same figure would let a 4 MB image
	// become a 5.3 MB body and fail at the provider instead of here. The raw limit
	// is derived from it (review asked which side the provider measures, and the
	// answer is that both fit if the raw size is three quarters).
	visionMaxEncodedBytes = 4 << 20

	// visionTagMin and visionTagMax are the range the prompt asks for. Fewer than
	// three is a thin answer; more than six is a tag cloud.
	visionTagMin = 3
	visionTagMax = 6
)

// visionSchema is the answer's shape, enforced by the provider as well as parsed
// here: a strict schema turns "the model wrote prose" into a rejected answer
// rather than a half-filled row.
var visionSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"kind", "tags", "ocr_text", "description", "colors"},
	"properties": map[string]any{
		"kind": map[string]any{"type": "string"},
		"tags": map[string]any{
			"type":     "array",
			"minItems": visionTagMin,
			"maxItems": visionTagMax,
			"items":    map[string]any{"type": "string"},
		},
		"ocr_text":    map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"colors": map[string]any{
			"type":     "array",
			"maxItems": 5,
			"items":    map[string]any{"type": "string"},
		},
	},
}

// Describe reads one image. It is enrich.Visioner for the real model.
//
// The image is sent as the original bytes, base64 inline: the provider sees what
// the library keeps, so OCR is done on the resolution that was saved rather than
// on a thumbnail that lost the small print.
func (c *Client) Describe(ctx context.Context, input enrich.VisionInput) (enrich.Vision, error) {
	if len(input.Image) == 0 {
		return enrich.Vision{}, errors.New("nova vision: no image")
	}
	// Three quarters of the body ceiling: 4 MB of base64 carries 3 MB of bytes.
	// The encoded length is the stdlib's own arithmetic rather than a 4/3
	// approximation, so the refusal quotes the number that would actually be sent.
	if encoded := base64.StdEncoding.EncodedLen(len(input.Image)); encoded > visionMaxEncodedBytes {
		// Not retried: the same bytes are the same size next time. The caller
		// stores the original and badges it, which is the documented fallback.
		return enrich.Vision{}, fmt.Errorf(
			"nova vision: the image is %d bytes, and base64 makes it %d, over the %d-byte request limit; not sent",
			len(input.Image), encoded, visionMaxEncodedBytes)
	}
	system, err := prompt(VisionPromptVersion + ".md")
	if err != nil {
		return enrich.Vision{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	mimeType := input.MimeType
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	resp, err := c.chat.Stream(ctx, provider.Request{
		Model: visionModel,
		Messages: []message.Message{
			message.System(system),
			message.UserParts(visionState(input), message.ImagePart(mimeType, base64.StdEncoding.EncodeToString(input.Image))),
		},
		Temperature: floatPtr(0.2),
		MaxTokens:   visionMaxTokens,
		ProviderOptions: map[string]any{
			"response_format": map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "harbor_image",
					"strict": true,
					"schema": visionSchema,
				},
			},
		},
	}, nil)
	if err != nil {
		if resp != nil {
			c.recordIfBilled(ctx, "vision", visionModel, resp.Model, resp.Usage)
		}
		return enrich.Vision{}, fmt.Errorf("nova vision: %w", err)
	}
	c.logUsage(ctx, "vision", visionModel, resp.Model, resp.Usage)

	// A cancelled call is an interruption, not the model's verdict on the image.
	if cerr := ctx.Err(); cerr != nil {
		return enrich.Vision{}, fmt.Errorf("nova vision: %w", cerr)
	}
	vision, parseErr := c.parseVision(resp.Message.Content)
	if parseErr != nil {
		if resp.FinishReason == finishLength {
			return enrich.Vision{}, fmt.Errorf("nova vision: the answer hit the %d-token cap and was cut off (%d completion tokens); not retried, because the same cap cuts it again",
				visionMaxTokens, resp.Usage.CompletionTokens)
		}
		return enrich.Vision{}, parseErr
	}
	return vision, nil
}

// visionState is the text half of the message: the conversation around the image.
// It carries the vocabulary and the filename, which are the only two things
// known before the model looks.
func visionState(input enrich.VisionInput) string {
	var b strings.Builder
	b.WriteString("Describe the attached image.\n")
	if name := strings.TrimSpace(input.Filename); name != "" {
		b.WriteString("\nFilename: " + name + "\n")
	}
	if len(input.Vocabulary) > 0 {
		b.WriteString("\nTags already in the library, reuse a spelling when one fits:\n")
		for _, tag := range input.Vocabulary {
			b.WriteString("- " + tag + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// parseVision reads the answer, and refuses one that cannot be filed: an empty
// description means the model described nothing, and a kind outside the twelve
// is coerced to image rather than failing the call, because the picture is what
// it is whether or not the model named it well.
func (c *Client) parseVision(raw string) (enrich.Vision, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		raw = raw[start : end+1]
	}

	var parsed struct {
		Kind        string   `json:"kind"`
		Tags        []string `json:"tags"`
		OCRText     string   `json:"ocr_text"`
		Description string   `json:"description"`
		Colors      []string `json:"colors"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return enrich.Vision{}, fmt.Errorf("nova vision: answer is not JSON: %w", err)
	}
	description := strings.TrimSpace(parsed.Description)
	if description == "" {
		return enrich.Vision{}, errors.New("nova vision: the answer described nothing")
	}

	kind := model.Kind(strings.ToLower(strings.TrimSpace(parsed.Kind)))
	if !kind.Valid() {
		c.log.Warn("vision returned a kind outside the twelve; using image", "kind", parsed.Kind)
		kind = model.KindImage
	}
	return enrich.Vision{
		Kind:        kind,
		Tags:        cleanVisionTags(parsed.Tags),
		OCRText:     strings.TrimSpace(parsed.OCRText),
		Description: description,
		Colors:      cleanColors(parsed.Colors),
	}, nil
}

// cleanVisionTags trims, lowercases and de-duplicates, and keeps the order the
// model gave so the most specific tags stay first. The range itself is the
// schema's job: a model that returns two is not a reason to fail a save.
func cleanVisionTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.Join(strings.Fields(tag), " "))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// cleanColors keeps only what looks like a hex colour: a card placeholder asking
// for "#ffd" or "pale blue" would produce a stylesheet that does nothing.
func cleanColors(colors []string) []string {
	out := make([]string, 0, len(colors))
	for _, c := range colors {
		c = strings.TrimSpace(strings.ToLower(c))
		if len(c) != 7 || c[0] != '#' {
			continue
		}
		if strings.Trim(c[1:], "0123456789abcdef") != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}
