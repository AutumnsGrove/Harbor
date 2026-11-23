package store

import (
	"context"
	"errors"
	"testing"
)

// plans/05 test 6: a cancelled run resumes where it stopped, for the same file,
// and nowhere else.

func TestStartImportRunBeginsAtZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	run, resumed, err := s.StartImportRun(ctx, "sha-a", `{"total":1}`)
	if err != nil {
		t.Fatalf("StartImportRun: %v", err)
	}
	if resumed {
		t.Error("a first run claimed to be a resume")
	}
	if run.State != ImportRunning || run.Checkpoint != 0 || run.CountsJSON != `{"total":1}` {
		t.Errorf("run = %+v", run)
	}
	if run.StartedAt.IsZero() || run.FinishedAt != nil {
		t.Errorf("timestamps = %v / %v", run.StartedAt, run.FinishedAt)
	}
}

func TestCancelledImportRunResumes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	run, _, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointImportRun(ctx, run.ID, 500, `{"total":500}`); err != nil {
		t.Fatalf("CheckpointImportRun: %v", err)
	}
	if err := s.FinishImportRun(ctx, run.ID, ImportCancelled, 500, `{"total":500}`); err != nil {
		t.Fatalf("FinishImportRun: %v", err)
	}

	resumed, isResume, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatalf("StartImportRun (resume): %v", err)
	}
	if !isResume {
		t.Error("a cancelled run was not resumed")
	}
	if resumed.ID != run.ID {
		t.Errorf("resumed %q, want the same run %q", resumed.ID, run.ID)
	}
	if resumed.Checkpoint != 500 || resumed.CountsJSON != `{"total":500}` {
		t.Errorf("resumed at %+v, want the checkpoint and counts kept", resumed)
	}
	if resumed.State != ImportRunning {
		t.Errorf("state = %q, want running", resumed.State)
	}
}

// A different file is a different run: its checkpoint means something else.
func TestADifferentFileStartsAFreshRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first, _, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointImportRun(ctx, first.ID, 700, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishImportRun(ctx, first.ID, ImportCancelled, 700, ""); err != nil {
		t.Fatal(err)
	}

	second, resumed, err := s.StartImportRun(ctx, "sha-b", "")
	if err != nil {
		t.Fatal(err)
	}
	if resumed || second.ID == first.ID || second.Checkpoint != 0 {
		t.Errorf("a new file resumed the old run: %+v (resumed %v)", second, resumed)
	}
}

// A done run is finished: importing the same file again is a fresh run whose
// rows all merge (the idempotence rule), not a resume from the end.
func TestAFinishedRunIsNotResumed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	run, _, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishImportRun(ctx, run.ID, ImportDone, 1476, ""); err != nil {
		t.Fatal(err)
	}

	again, resumed, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if resumed || again.ID == run.ID || again.Checkpoint != 0 {
		t.Errorf("a done run was resumed: %+v", again)
	}
	if n := count(t, s.db, `SELECT count(*) FROM import_runs`); n != 2 {
		t.Errorf("%d runs, want 2", n)
	}
}

func TestCheckpointAndFinishRejectBadInput(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	run, _, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.CheckpointImportRun(ctx, run.ID, -1, ""); err == nil {
		t.Error("a negative checkpoint was accepted")
	}
	if err := s.FinishImportRun(ctx, run.ID, "running", 0, ""); err == nil {
		t.Error("a non-final state was accepted")
	}
	if err := s.CheckpointImportRun(ctx, "nope", 1, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("checkpointing an unknown run = %v, want ErrNotFound", err)
	}
	if _, _, err := s.StartImportRun(ctx, "", ""); err == nil {
		t.Error("a run without a file hash was accepted")
	}
}

func TestLatestImportRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.LatestImportRun(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound on an empty table", err)
	}

	first, _, err := s.StartImportRun(ctx, "sha-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishImportRun(ctx, first.ID, ImportDone, 10, ""); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.StartImportRun(ctx, "sha-b", "")
	if err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestImportRun(ctx)
	if err != nil {
		t.Fatalf("LatestImportRun: %v", err)
	}
	if latest.ID != second.ID {
		t.Errorf("latest = %q, want the newer run %q", latest.ID, second.ID)
	}
}
