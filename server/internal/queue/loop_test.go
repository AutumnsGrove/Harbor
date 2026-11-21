package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

// plans/02 test 3 (backoff) and test 8's "a 401 pauses the loop rather than
// spinning". step() is the whole policy, so these tests never sleep.

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 20; attempt++ {
		delay := Backoff(attempt)
		if delay < previous {
			t.Errorf("Backoff(%d) = %v, less than Backoff(%d) = %v", attempt, delay, attempt-1, previous)
		}
		if delay > backoffMax {
			t.Errorf("Backoff(%d) = %v, above the cap %v", attempt, delay, backoffMax)
		}
		previous = delay
	}
	if got, want := Backoff(1), backoffBase; got != want {
		t.Errorf("Backoff(1) = %v, want %v", got, want)
	}
	if got, want := Backoff(4), 8*backoffBase; got != want {
		t.Errorf("Backoff(4) = %v, want %v", got, want)
	}
	if got := Backoff(100); got != backoffMax {
		t.Errorf("Backoff(100) = %v, want the cap %v", got, backoffMax)
	}
	// A defensive floor: a zero or negative attempt is the first failure.
	if Backoff(0) != Backoff(1) || Backoff(-3) != Backoff(1) {
		t.Error("Backoff does not floor at attempt 1")
	}
}

func TestJitterSpreadsButStaysBounded(t *testing.T) {
	for _, d := range []time.Duration{time.Second, PollCadence, backoffMax} {
		for n := 0; n < 50; n++ {
			got := defaultJitter(d)
			if got < d || got > d+d/4 {
				t.Fatalf("jitter(%v) = %v, outside [%v, %v]", d, got, d, d+d/4)
			}
		}
	}
	if defaultJitter(0) != 0 {
		t.Error("jitter(0) is not zero")
	}
}

func newTestLoop(t *testing.T, f *fakeWorker) *Loop {
	t.Helper()
	ing := NewIngestor(f, openStore(t), nil)
	loop := NewLoop(ing)
	loop.jitter = func(d time.Duration) time.Duration { return d }
	loop.cadence = time.Minute
	return loop
}

func TestStepBacksOffOnFailureAndResetsOnSuccess(t *testing.T) {
	f := &fakeWorker{}
	loop := newTestLoop(t, f)
	ctx := context.Background()

	f.claimErr = errors.New("connection refused")
	if got, want := loop.step(ctx), backoffBase; got != want {
		t.Errorf("first failure delay = %v, want %v", got, want)
	}
	if got, want := loop.step(ctx), 2*backoffBase; got != want {
		t.Errorf("second failure delay = %v, want %v", got, want)
	}
	status := loop.Status()
	if status.LastError == "" || status.Paused {
		t.Errorf("status after failures = %+v, want an error and not paused", status)
	}
	if status.LastPollAt.IsZero() || status.NextPollAt.IsZero() {
		t.Errorf("status = %+v, want poll times", status)
	}

	f.claimErr = nil
	if got, want := loop.step(ctx), loop.cadence; got != want {
		t.Errorf("delay after success = %v, want the cadence %v", got, want)
	}
	if status := loop.Status(); status.LastError != "" || status.LastOKAt.IsZero() {
		t.Errorf("status after success = %+v, want the error cleared", status)
	}

	// The backoff starts over rather than continuing from where it was.
	f.claimErr = errors.New("down again")
	if got, want := loop.step(ctx), backoffBase; got != want {
		t.Errorf("delay after a new failure = %v, want %v", got, want)
	}
}

func TestStepPausesOnUnauthorized(t *testing.T) {
	f := &fakeWorker{claimErr: &workerclient.Error{Status: 401, Message: "unauthorized"}}
	loop := newTestLoop(t, f)

	if got, want := loop.step(context.Background()), loop.cadence; got != want {
		t.Errorf("delay on 401 = %v, want the cadence %v (not a hot loop)", got, want)
	}
	status := loop.Status()
	if !status.Paused {
		t.Error("status.Paused = false after a 401")
	}
	if status.LastError == "" {
		t.Error("status.LastError is empty after a 401")
	}
}

func TestStepCapsTheBackoff(t *testing.T) {
	f := &fakeWorker{claimErr: errors.New("down")}
	loop := newTestLoop(t, f)
	loop.maxBackoff = time.Second
	for n := 0; n < 30; n++ {
		if got := loop.step(context.Background()); got > loop.maxBackoff {
			t.Fatalf("step %d returned %v, above maxBackoff %v", n, got, loop.maxBackoff)
		}
	}
}

// The UI opening calls Refresh; a save made 29 seconds into the cadence must
// not wait for the timer.
func TestRefreshWakesTheLoop(t *testing.T) {
	f := &fakeWorker{}
	loop := newTestLoop(t, f)
	loop.cadence = time.Hour // so only a Refresh can produce a second poll

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool { return f.claimCount() >= 1 })
	loop.Refresh()
	waitFor(t, 2*time.Second, func() bool { return f.claimCount() >= 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before the timeout")
}
