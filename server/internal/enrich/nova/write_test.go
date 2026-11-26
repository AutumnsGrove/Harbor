package nova

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
)

// The eight rows that produced issue #78 had spent all three item attempts on
// calls that answered with nothing, so a provider miss was indistinguishable
// from a page that will not read. These tests pin the difference: the call is
// asked again, and the item is charged once.

const goodAnswer = `{"title":"Medieval cat law","summary":"A survey of feline legislation.","tags":["cats","history","law"]}`

// scriptedChat answers each call from a list, so a test can say what the second
// answer is. An extra call is a failure, not a silent success.
type scriptedChat struct {
	answers []scriptedAnswer
	calls   int
	// onCall runs before the answer is returned, so a test can interrupt the call
	// itself rather than the loop around it.
	onCall func(call int)
}

type scriptedAnswer struct {
	content string
	finish  string
	usage   provider.Usage
	err     error
}

func (s *scriptedChat) Name() string { return "scripted" }

func (s *scriptedChat) Stream(_ context.Context, _ provider.Request, _ func(provider.StreamEvent)) (*provider.Response, error) {
	s.calls++
	if s.calls > len(s.answers) {
		return nil, errors.New("the client made more calls than the test scripted")
	}
	call := s.calls
	if s.onCall != nil {
		s.onCall(call)
	}
	answer := s.answers[call-1]
	if answer.err != nil {
		return nil, answer.err
	}
	return &provider.Response{
		Message:      message.Assistant(answer.content),
		FinishReason: answer.finish,
		Usage:        answer.usage,
		Model:        chatModel,
	}, nil
}

func writeClient(chat *scriptedChat, sink enrich.UsageSink) *Client {
	return &Client{
		chat:    chat,
		log:     slog.New(slog.DiscardHandler),
		timeout: time.Second,
		usage:   sink,
		now:     time.Now,
	}
}

func writeInput() enrich.ContentInput {
	return enrich.ContentInput{URL: "https://example.test/law", Text: "A page about cats."}
}

func TestWriteAsksAgainWhenTheAnswerIsEmpty(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: "", finish: "stop", usage: provider.Usage{CompletionTokens: 0, CostUSD: 0.0001}},
		{content: goodAnswer, finish: "stop", usage: provider.Usage{CompletionTokens: 40, CostUSD: 0.0002}},
	}}
	sink := &recordingSink{}

	content, err := writeClient(chat, sink).Write(context.Background(), writeInput())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if chat.calls != 2 {
		t.Errorf("calls = %d, want the empty answer asked again", chat.calls)
	}
	if content.Summary == "" || len(content.Tags) != 3 {
		t.Errorf("content = %+v, want the second answer's fields", content)
	}
	// Both calls were made, so both were billed, so both are recorded.
	if len(sink.rows) != 2 {
		t.Errorf("usage rows = %d, want one per call", len(sink.rows))
	}
}

func TestWriteGivesUpAfterTwoEmptyAnswers(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: "   ", finish: "stop", usage: provider.Usage{CompletionTokens: 0}},
		{content: "", finish: "stop", usage: provider.Usage{CompletionTokens: 0}},
	}}

	_, err := writeClient(chat, nil).Write(context.Background(), writeInput())
	if err == nil {
		t.Fatal("two empty answers were accepted")
	}
	if chat.calls != writeCallAttempts {
		t.Errorf("calls = %d, want %d", chat.calls, writeCallAttempts)
	}
	if !strings.Contains(err.Error(), "answered with nothing") {
		t.Errorf("error = %q, want it to say the model answered nothing", err)
	}
	for _, want := range []string{"finish stop", "asked 2 times"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "is not JSON") {
		t.Errorf("error = %q still blames the JSON", err)
	}
}

// A cut-off answer is deterministic: the same input and the same cap cut it in
// the same place, so a second call pays for the same truncation.
func TestWriteDoesNotRetryATruncatedAnswer(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: `{"title":"Medieval cat law","summary":"A surv`, finish: "length", usage: provider.Usage{CompletionTokens: writeMaxTokens}},
		{content: goodAnswer, finish: "stop"},
	}}

	_, err := writeClient(chat, nil).Write(context.Background(), writeInput())
	if err == nil {
		t.Fatal("a truncated answer was accepted")
	}
	if chat.calls != 1 {
		t.Errorf("calls = %d, want no retry for a truncated answer", chat.calls)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d-token cap", writeMaxTokens)) {
		t.Errorf("error = %q, want it to name the cap to change", err)
	}
}

// Prose around no JSON is a shapeless answer too, and reads differently in the
// log from an empty one.
func TestWriteNamesWhatTheModelDidInstead(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: "Sure! Here is the JSON you asked for:", finish: "stop", usage: provider.Usage{CompletionTokens: 9}},
		{content: "Sure! Here is the JSON you asked for:", finish: "stop", usage: provider.Usage{CompletionTokens: 9}},
	}}

	_, err := writeClient(chat, nil).Write(context.Background(), writeInput())
	if err == nil {
		t.Fatal("a prose answer was accepted")
	}
	if chat.calls != 2 {
		t.Errorf("calls = %d, want the prose answer retried", chat.calls)
	}
	for _, want := range []string{"was not JSON", "finish stop", "Sure!"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// A call that never arrives is the pipeline's retry to spend. Retrying it here
// would bill the dead network twice and halve the item's attempts for nothing.
func TestWriteDoesNotRetryATransportFailure(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{err: errors.New("dial tcp: connection refused")},
		{content: goodAnswer, finish: "stop"},
	}}

	_, err := writeClient(chat, nil).Write(context.Background(), writeInput())
	if err == nil {
		t.Fatal("a transport failure was swallowed")
	}
	if chat.calls != 1 {
		t.Errorf("calls = %d, want no retry for a transport failure", chat.calls)
	}
	if !strings.Contains(err.Error(), "nova write") || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %q, want it wrapped and readable", err)
	}
}

// A cancelled context must stop the retry loop. The assertion is the call count:
// the loop cannot exceed writeCallAttempts, so "more than the bound" can never
// fail and would let the guard be deleted without a test noticing (review on #86).
func TestWriteStopsRetryingWhenTheContextDies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := &scriptedChat{
		answers: []scriptedAnswer{
			{content: "", finish: "stop"},
			{content: goodAnswer, finish: "stop"},
		},
		onCall: func(int) { cancel() },
	}

	_, err := writeClient(chat, nil).Write(ctx, writeInput())
	if err == nil {
		t.Fatal("Write answered under a cancelled context")
	}
	if chat.calls != 1 {
		t.Fatalf("calls = %d, want 1: a dead context is not asked again", chat.calls)
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %q, want the cancellation", err)
	}
	if strings.Contains(err.Error(), "the page was not the problem") {
		t.Errorf("error = %q blames the page for a cancelled run", err)
	}
}

// An answer that arrives after the context died is not an outcome. The pipeline
// is stopping; recording the answer would make an interruption look like success.
func TestWriteDiscardsAnAnswerAfterTheContextDies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := &scriptedChat{
		answers: []scriptedAnswer{{content: goodAnswer, finish: "stop"}},
		onCall:  func(int) { cancel() },
	}

	content, err := writeClient(chat, nil).Write(ctx, writeInput())
	if err == nil {
		t.Fatalf("a cancelled call's answer was accepted: %+v", content)
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %q, want the cancellation", err)
	}
	if chat.calls != 1 {
		t.Errorf("calls = %d, want 1", chat.calls)
	}
}

// head() puts the model's opening bytes into items.last_error, which the attention
// strip shows. A model answering in Japanese must not leave a broken rune there
// (issue #90).
func TestHeadCutsOnARune(t *testing.T) {
	answer := strings.Repeat("応答はこちらです", 20)
	got := head(answer, 80)
	if len(got) > 80 {
		t.Errorf("head is %d bytes, want at most 80", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("head is not valid UTF-8: %q", got)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Errorf("head carries a replacement character: %q", got)
	}
	// Newlines collapse: the error stays one line.
	if got := head("first\nsecond", 80); got != "first second" {
		t.Errorf("head did not collapse whitespace: %q", got)
	}
}
