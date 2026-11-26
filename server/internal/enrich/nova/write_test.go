package nova

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	answer := s.answers[s.calls-1]
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
	if !strings.Contains(err.Error(), "800-token cap") {
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

// A cancelled context must not turn into a retry loop: the answer is not coming.
func TestWriteStopsRetryingWhenTheContextDies(t *testing.T) {
	chat := &scriptedChat{answers: []scriptedAnswer{
		{content: "", finish: "stop"},
		{content: "", finish: "stop"},
	}}
	client := writeClient(chat, nil)
	client.timeout = time.Nanosecond // the call's budget is gone before the first answer

	if _, err := client.Write(context.Background(), writeInput()); err == nil {
		t.Fatal("Write answered under an expired deadline")
	}
	if chat.calls > writeCallAttempts {
		t.Errorf("calls = %d, want at most %d", chat.calls, writeCallAttempts)
	}
}
