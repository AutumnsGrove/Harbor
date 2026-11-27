package nova

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/06-media test 6: the vision answer is one JSON object with five fields,
// and a malformed one fails the call rather than writing a half-filled row.

const goodVision = `{"kind":"image","tags":["Receipt"," groceries ","receipt"],"ocr_text":"TOTAL 12.40","description":"A grocery receipt photographed on a table.","colors":["#f2e8dc","#3a3a3a"]}`

func visionClient(chat *scriptedChat, sink enrich.UsageSink) *Client {
	return &Client{
		chat:    chat,
		log:     slog.New(slog.DiscardHandler),
		timeout: time.Second,
		usage:   sink,
		now:     time.Now,
	}
}

func visionInput() enrich.VisionInput {
	return enrich.VisionInput{
		Image:      []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'},
		MimeType:   "image/jpeg",
		Filename:   "IMG_1861.JPG",
		Vocabulary: []string{"groceries", "receipts"},
	}
}

func TestDescribeParsesTheFiveFields(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: goodVision, finish: "stop", usage: provider.Usage{CompletionTokens: 60, CostUSD: 0.0004}},
	}}
	sink := &recordingSink{}

	vision, err := visionClient(chat, sink).Describe(context.Background(), visionInput())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if vision.Kind != model.KindImage {
		t.Errorf("kind = %q", vision.Kind)
	}
	if vision.OCRText != "TOTAL 12.40" {
		t.Errorf("ocr_text = %q, want the transcription kept exactly", vision.OCRText)
	}
	if vision.Description == "" {
		t.Error("description is empty")
	}
	// Tags are lowercased, trimmed and de-duplicated, and the model's order kept.
	want := []string{"receipt", "groceries"}
	if len(vision.Tags) != len(want) || vision.Tags[0] != want[0] || vision.Tags[1] != want[1] {
		t.Errorf("tags = %v, want %v", vision.Tags, want)
	}
	if len(vision.Colors) != 2 || vision.Colors[0] != "#f2e8dc" {
		t.Errorf("colors = %v", vision.Colors)
	}
	// The call is billed and recorded like any other (issue #52).
	if len(sink.rows) != 1 {
		t.Errorf("usage rows = %d, want 1", len(sink.rows))
	} else if sink.rows[0].Surface != "vision" {
		t.Errorf("usage surface = %q, want vision", sink.rows[0].Surface)
	}
}

// The image has to reach the model as the bytes the library keeps, base64 inline,
// with the type the caller sniffed rather than one a server claimed.
func TestDescribeSendsTheImageItWasGiven(t *testing.T) {
	var got provider.Request
	chat := &scriptedChat{answers: []scriptedAnswer{{content: goodVision, finish: "stop"}}}
	chat.onRequest = func(req provider.Request) { got = req }

	input := visionInput()
	if _, err := visionClient(chat, nil).Describe(context.Background(), input); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	// The system prompt is a plain string; the user message carries the image.
	if len(got.Messages) != 2 {
		t.Fatalf("%d messages, want a system and a user message", len(got.Messages))
	}
	user := got.Messages[1]
	if len(user.Parts) != 1 {
		t.Fatalf("%d parts, want the image", len(user.Parts))
	}
	part := user.Parts[0]
	if part.Type != "image" || part.MimeType != "image/jpeg" {
		t.Errorf("part = %+v, want an image/jpeg part", part)
	}
	want := base64.StdEncoding.EncodeToString(input.Image)
	if part.Data != want {
		t.Errorf("image data = %q, want the base64 of the bytes given", part.Data)
	}
	// The text half carries the vocabulary and the filename, which are the only
	// things known before the model looks.
	if !strings.Contains(user.Content, "groceries") || !strings.Contains(user.Content, "IMG_1861.JPG") {
		t.Errorf("the user text lost the vocabulary or the filename: %q", user.Content)
	}
	if got.ProviderOptions["response_format"] == nil {
		t.Error("the call did not ask for a JSON schema answer")
	}
}

// A "kind" outside the twelve is coerced to image rather than failing the call:
// the picture is what it is whether or not the model named it well.
func TestDescribeCoercesAnInventedKind(t *testing.T) {
	answer := `{"kind":"screenshot","tags":["a","b","c"],"ocr_text":"","description":"A screenshot of a settings page.","colors":[]}`
	chat := &scriptedChat{answers: []scriptedAnswer{{content: answer, finish: "stop"}}}
	vision, err := visionClient(chat, nil).Describe(context.Background(), visionInput())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if vision.Kind != model.KindImage {
		t.Errorf("kind = %q, want image", vision.Kind)
	}
}

// An answer that describes nothing is not an answer: the row would carry an
// empty description forever, so the call fails and the pipeline retries or badges.
func TestDescribeRefusesAnEmptyDescription(t *testing.T) {
	answer := `{"kind":"image","tags":["a","b","c"],"ocr_text":"","description":"   ","colors":[]}`
	chat := &scriptedChat{answers: []scriptedAnswer{{content: answer, finish: "stop"}}}
	if _, err := visionClient(chat, nil).Describe(context.Background(), visionInput()); err == nil {
		t.Fatal("an empty description was accepted")
	}
}

// Prose where JSON was asked for is a malformed answer, and a cut-off answer is
// named as the cap rather than as bad JSON (issue #78's lesson).
func TestDescribeNamesWhyAnAnswerIsUnusable(t *testing.T) {
	prose := &scriptedChat{answers: []scriptedAnswer{{content: "This is a receipt.", finish: "stop"}}}
	if _, err := visionClient(prose, nil).Describe(context.Background(), visionInput()); err == nil {
		t.Fatal("prose was accepted as an answer")
	}

	cutOff := &scriptedChat{answers: []scriptedAnswer{{
		content: `{"kind":"image","tags":["a"`,
		finish:  finishLength,
		usage:   provider.Usage{CompletionTokens: visionMaxTokens},
	}}}
	_, err := visionClient(cutOff, nil).Describe(context.Background(), visionInput())
	if err == nil {
		t.Fatal("a cut-off answer was accepted")
	}
	if !strings.Contains(err.Error(), "token cap") {
		t.Errorf("error = %v, want the cap named", err)
	}
	// And it is not asked again: the same cap cuts it in the same place.
	if cutOff.calls != 1 {
		t.Errorf("%d calls, want 1", cutOff.calls)
	}
}

// A body over the provider's inline limit is refused before the request, because
// the same bytes are the same size next time.
func TestDescribeRefusesAnImageTooLargeToSend(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{{content: goodVision, finish: "stop"}}}
	input := visionInput()
	// One byte over what base64 can carry: the limit is on the encoded body, so
	// the raw ceiling is three quarters of it.
	input.Image = make([]byte, visionMaxEncodedBytes/4*3+1)
	_, err := visionClient(chat, nil).Describe(context.Background(), input)
	if err == nil {
		t.Fatal("an oversized image was sent")
	}
	if !strings.Contains(err.Error(), "request limit") {
		t.Errorf("error = %v, want the limit named", err)
	}
	if chat.calls != 0 {
		t.Errorf("%d calls, want none", chat.calls)
	}
	// An empty image is not a call either.
	if _, err := visionClient(chat, nil).Describe(context.Background(), enrich.VisionInput{}); err == nil {
		t.Error("an empty image was accepted")
	}
}

// A cancelled call is an interruption, not the model's verdict on the picture.
func TestDescribeReportsACancellationRatherThanAnAnswer(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{{content: goodVision, finish: "stop"}}}
	ctx, cancel := context.WithCancel(context.Background())
	chat.onCall = func(int) { cancel() }
	if _, err := visionClient(chat, nil).Describe(ctx, visionInput()); err == nil {
		t.Fatal("a cancelled call returned an answer")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
}

// Colours that are not hex are dropped: a placeholder asking for "pale blue"
// produces a stylesheet that does nothing.
func TestCleanColorsKeepsOnlyHex(t *testing.T) {
	got := cleanColors([]string{"#f2e8DC", "pale blue", "#fff", "#12345g", " #3a3a3a ", ""})
	want := []string{"#f2e8dc", "#3a3a3a"}
	if len(got) != len(want) {
		t.Fatalf("colors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("colors = %v, want %v", got, want)
		}
	}
}

// Every model kind survives a round trip through the prompt's own list, so a
// picture of a book cover can be filed as a book.
func TestDescribeKeepsAValidKind(t *testing.T) {
	for _, kind := range []model.Kind{model.KindImage, model.KindBook, model.KindArticle, model.KindPaper} {
		answer := `{"kind":"` + string(kind) + `","tags":["a","b","c"],"ocr_text":"","description":"Something.","colors":[]}`
		chat := &scriptedChat{answers: []scriptedAnswer{{content: answer, finish: "stop"}}}
		vision, err := visionClient(chat, nil).Describe(context.Background(), visionInput())
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if vision.Kind != kind {
			t.Errorf("kind = %q, want %q", vision.Kind, kind)
		}
	}
}

// The refusal quotes the length that would actually be sent, not a rounded
// approximation: "4194304 bytes, over the 4194304-byte limit" is the kind of
// message that makes a reader doubt the code (a point from review).
func TestTheOversizeRefusalQuotesTheEncodedLength(t *testing.T) {
	chat := &scriptedChat{}
	input := visionInput()
	input.Image = make([]byte, visionMaxEncodedBytes/4*3+1)
	_, err := visionClient(chat, nil).Describe(context.Background(), input)
	if err != nil {
		// The message must name the encoded length and the limit, and the length
		// it names must be strictly greater than the limit.
		encoded := base64.StdEncoding.EncodedLen(len(input.Image))
		if !strings.Contains(err.Error(), "base64 makes it") {
			t.Fatalf("error = %v, want the encoded length named", err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(encoded)) {
			t.Errorf("error = %v, want it to quote %d", err, encoded)
		}
		if strings.Contains(err.Error(), "4194304 bytes, and base64 makes it 4194304") {
			t.Errorf("the message quotes the limit as the size: %v", err)
		}
		if chat.calls != 0 {
			t.Errorf("%d calls, want none", chat.calls)
		}
		return
	}
	t.Fatal("an oversized image was accepted")
}
