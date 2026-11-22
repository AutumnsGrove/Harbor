package enrich

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The scored set replays pages that are already extracted, so it has to build
// exactly the inputs production builds — one place decides the caps, or the
// score measures a prompt nobody ships (plans/03, "Scored fixture set").

func TestClassifyForIsTheClassifyCap(t *testing.T) {
	page := PageText{
		URL:      "https://kitchen.example/pancakes",
		Title:    "Pancakes",
		SiteName: "Kitchen",
		Excerpt:  "A use for the discard.",
		Text:     strings.Repeat("a", classifyTextLimit+500),
	}
	input := ClassifyFor(page)
	if len(input.Text) != classifyTextLimit {
		t.Errorf("classify text = %d bytes, want the %d-byte cap", len(input.Text), classifyTextLimit)
	}
	if input.URL != page.URL || input.Title != page.Title || input.SiteName != page.SiteName || input.Excerpt != page.Excerpt {
		t.Errorf("metadata lost: %+v", input)
	}
	if input.IsNote {
		t.Error("a page with a URL is not a note")
	}
}

func TestContentForIsTheChatCap(t *testing.T) {
	page := PageText{Text: strings.Repeat("b", chatTextLimit+500)}
	input := ContentFor(page, "recipe")
	if len(input.Text) != chatTextLimit {
		t.Errorf("chat text = %d bytes, want the %d-byte cap", len(input.Text), chatTextLimit)
	}
	if input.Kind != "recipe" {
		t.Errorf("kind = %q, want the one passed in", input.Kind)
	}
}

func TestACapDoesNotSplitARune(t *testing.T) {
	// Each "€" is three bytes, so a plain byte slice at the cap lands inside
	// one and the model is sent invalid UTF-8.
	page := PageText{Text: strings.Repeat("e", classifyTextLimit-1) + "€€€"}
	input := ClassifyFor(page)
	if !utf8.ValidString(input.Text) {
		t.Errorf("classify text is not valid UTF-8: %q", input.Text[len(input.Text)-6:])
	}
	if len(input.Text) > classifyTextLimit {
		t.Errorf("classify text = %d bytes, over the %d-byte cap", len(input.Text), classifyTextLimit)
	}
}

func TestProcessSendsTheCappedTextToBothCalls(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	long := strings.Repeat("Whisk the starter. ", 2_000)
	item := createItem(t, s, store.ItemInput{ID: "long", URL: ptr("https://kitchen.example/long")})
	f, d, w, em := happyFakes()
	f.body = `<html><head><title>Long</title></head><body><article><h1>Long</h1><p>` + long + `</p></article></body></html>`
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(d.last.Text) > classifyTextLimit {
		t.Errorf("decide saw %d bytes, cap %d", len(d.last.Text), classifyTextLimit)
	}
	if len(w.last.Text) > chatTextLimit {
		t.Errorf("write saw %d bytes, cap %d", len(w.last.Text), chatTextLimit)
	}
	if len(d.last.Text) == 0 || len(w.last.Text) == 0 {
		t.Fatal("one of the calls saw no page text")
	}
}
