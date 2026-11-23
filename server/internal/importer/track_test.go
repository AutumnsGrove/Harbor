package importer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// RunTracked is the bookkeeping both interfaces share (plans/05, "Resumability"),
// so it is tested here rather than twice through the CLI and the API: a run is
// closed as done or cancelled, the checkpoint survives an interrupt, and a
// cancelled run resumes from it.

func (e *env) track(t *testing.T, sum, body string, opts Options) (Report, Tracked, error) {
	t.Helper()
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	return RunTracked(e.ctx, e.store, sum, src, opts)
}

const twoRows = `1,One,,,https://example.com/one,Recipes,,2020-01-02T03:04:05Z,,,false
2,Two,,,https://example.com/two,,,2020-01-03T03:04:05Z,,,false
`

func TestRunTrackedClosesTheRunAsDone(t *testing.T) {
	e := newEnv(t)
	report, tracked, err := e.track(t, "sha-one", twoRows, Options{})
	if err != nil {
		t.Fatalf("RunTracked: %v", err)
	}
	if report.Imported != 2 || tracked.Stopped || tracked.Resumed {
		t.Fatalf("report %+v tracked %+v, want two imported and no resume", report, tracked)
	}

	run, err := e.store.ImportRunByID(e.ctx, tracked.RunID)
	if err != nil {
		t.Fatalf("ImportRunByID: %v", err)
	}
	if run.State != store.ImportDone || run.Checkpoint != 2 || run.FinishedAt == nil {
		t.Errorf("run = %+v, want done at record 2 with a finish time", run)
	}
	if !strings.Contains(run.CountsJSON, `"imported":2`) {
		t.Errorf("counts = %s, want the report's numbers", run.CountsJSON)
	}
}

// A cancelled context is a controlled stop, not an error: the row is closed as
// cancelled at the record it reached, on a context that survives the interrupt.
func TestRunTrackedClosesACancelledRun(t *testing.T) {
	e := newEnv(t)

	// The interrupt lands inside the source, which is where a real Ctrl-C lands:
	// the run row is already open, and everything written after it — the final
	// state above all — has to ignore the dead context.
	ctx, cancel := context.WithCancel(e.ctx)
	src, err := NewCSVSource(strings.NewReader(header + twoRows))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	report, tracked, err := RunTracked(ctx, e.store, "sha-cancel", &cancelAfter{Source: src, cancel: cancel}, Options{})
	if err != nil {
		t.Fatalf("RunTracked on a cancelled context: %v", err)
	}
	if !tracked.Stopped || report.Total != 0 {
		t.Fatalf("tracked %+v report %+v, want a stopped run that read nothing", tracked, report)
	}

	run, err := e.store.ImportRunByID(e.ctx, tracked.RunID)
	if err != nil {
		t.Fatalf("ImportRunByID: %v", err)
	}
	if run.State != store.ImportCancelled {
		t.Errorf("state = %q, want cancelled", run.State)
	}
	if run.FinishedAt == nil {
		t.Error("finished_at is nil; the run was left open")
	}
}

// The checkpoint is written while the run is going, so a crash mid-file is
// resumable; and a resume starts behind it instead of re-reading the file.
func TestRunTrackedResumesFromItsCheckpoint(t *testing.T) {
	e := newEnv(t)

	body := `1,One,,,https://example.com/one,Recipes,,2020-01-02T03:04:05Z,,,false
2,Two,,,https://example.com/two,,,2020-01-03T03:04:05Z,,,false
3,Three,,,https://example.com/three,,,2020-01-04T03:04:05Z,,,false
`

	// A run stopped after two rows, as an interrupt leaves it.
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	stopped, cancel := context.WithCancel(e.ctx)
	// CheckpointEvery is 1 so the checkpoints this test reads are the ones the
	// run writes, not the default every hundred rows.
	_, tracked, err := RunTracked(stopped, e.store, "sha-resume", &cancelAfter{Source: src, cancel: cancel, after: 2}, Options{CheckpointEvery: 1})
	if err != nil {
		t.Fatalf("RunTracked: %v", err)
	}
	if !tracked.Stopped {
		t.Fatal("the run was not stopped")
	}
	run, err := e.store.ImportRunByID(e.ctx, tracked.RunID)
	if err != nil {
		t.Fatalf("ImportRunByID: %v", err)
	}
	if run.Checkpoint != 2 {
		t.Fatalf("checkpoint = %d, want 2: the interrupt lands between rows", run.Checkpoint)
	}

	// The same file resumes from record 2 and reads only the row after it.
	src2, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	report, resumed, err := RunTracked(e.ctx, e.store, "sha-resume", src2, Options{})
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !resumed.Resumed || resumed.From != 2 {
		t.Errorf("tracked %+v, want a resume from record 2", resumed)
	}
	if report.Total != 1 || resumed.RunID != tracked.RunID {
		t.Errorf("report %+v tracked %+v, want the one row after the checkpoint in the same run", report, resumed)
	}
	if n := len(e.library(t).Items); n != 3 {
		t.Errorf("%d items, want 3: two from the first pass and the one it resumed at", n)
	}
}

// cancelAfter cancels the run's context once it has yielded N rows, which is
// where a Ctrl-C lands: between records, after the checkpoint.
type cancelAfter struct {
	Source
	cancel context.CancelFunc
	after  int
	read   int
}

func (c *cancelAfter) Next() (Row, error) {
	if c.read >= c.after {
		c.cancel()
		return Row{}, context.Canceled
	}
	row, err := c.Source.Next()
	c.read++
	return row, err
}

// An error the reader cannot recover from still closes the run, so it is never
// left saying "running" for a status poll to report forever.
func TestRunTrackedClosesTheRunWhenTheReaderFails(t *testing.T) {
	e := newEnv(t)
	src, err := NewCSVSource(strings.NewReader(header))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	_, tracked, err := RunTracked(e.ctx, e.store, "sha-broken", brokenSource{Source: src}, Options{})
	if !errors.Is(err, errBrokenReader) {
		t.Fatalf("RunTracked error = %v, want the reader's error", err)
	}
	run, readErr := e.store.ImportRunByID(e.ctx, tracked.RunID)
	if readErr != nil {
		t.Fatalf("ImportRunByID: %v", readErr)
	}
	if run.State != store.ImportDone || run.FinishedAt == nil {
		t.Errorf("run = %+v, want it closed as done", run)
	}
}

var errBrokenReader = errors.New("the disk went away")

type brokenSource struct{ Source }

func (b brokenSource) Next() (Row, error) { return Row{}, errBrokenReader }
