package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// plans/12a, "Tests first" 4: the snapshot, its retention, and the rule that a
// backup which cannot be verified is never deleted.
//
// These run against a real SQLite file because the point is the file: a fake
// would prove nothing about VACUUM INTO, zstd, or integrity_check.

var fixedNow = time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)

func clock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// snapFunc is a store whose Snapshot is a function, so a test can make it fail.
type snapFunc func(ctx context.Context, dest string) error

func (f snapFunc) Snapshot(ctx context.Context, dest string) error { return f(ctx, dest) }

// realSnapshotter is the production path: a real SQLite snapshot, so a test can
// decompress what Create wrote and open it.
func realSnapshotter(t *testing.T) (snapFunc, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.CreateItem(context.Background(), store.ItemInput{ID: "a", Note: "a thought"}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	return snapFunc(s.Snapshot), dir
}

func decompress(t *testing.T, path string) string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer in.Close()
	decoder, err := zstd.NewReader(in)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer decoder.Close()

	out := filepath.Join(t.TempDir(), "restored.db")
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, decoder); err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateWritesAVerifiableBackup(t *testing.T) {
	snapshotter, dataDir := realSnapshotter(t)
	backups := filepath.Join(dataDir, "backups")

	result, err := Create(context.Background(), snapshotter, backups, clock(fixedNow), DefaultKeep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(result.Kept) != 1 {
		t.Fatalf("result = %+v, want one", result)
	}
	want := "harbor-20261122T180000Z.db.zst"
	if result.Kept[0].Name != want {
		t.Errorf("name = %q, want %q", result.Kept[0].Name, want)
	}

	// The compressed file must be a database that passes integrity_check, which
	// is the only thing that makes calling it a backup honest.
	restored := decompress(t, result.Kept[0].Path)
	if err := store.Verify(restored); err != nil {
		t.Fatalf("the backup does not verify: %v", err)
	}

	// And nothing else is left in the directory: no temp snapshot, no temp zst.
	left, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		names := make([]string, len(left))
		for i, entry := range left {
			names[i] = entry.Name()
		}
		t.Errorf("backups dir = %v, want exactly the one file", names)
	}
}

func TestCreateKeepsThirtyAndPrunesTheRest(t *testing.T) {
	snapshotter, dataDir := realSnapshotter(t)
	backups := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	// Thirty-one days of backups that are each a real, verifiable snapshot.
	good := filepath.Join(dataDir, "seed.db")
	if err := snapshotter.Snapshot(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 31; day++ {
		name := prefix + fixedNow.AddDate(0, 0, -day).Format(nameLayout) + suffix
		if err := compress(good, filepath.Join(backups, name)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Create(context.Background(), snapshotter, backups, clock(fixedNow), DefaultKeep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(result.Kept) != DefaultKeep {
		t.Errorf("kept %d backups, want %d", len(result.Kept), DefaultKeep)
	}
	if result.Kept[0].Date != fixedNow {
		t.Errorf("newest = %v, want the one just written", result.Kept[0].Date)
	}
	if len(result.Failed) != 0 {
		t.Errorf("failures = %+v, want none", result.Failed)
	}
	oldest := fixedNow.AddDate(0, 0, -31)
	for _, entry := range result.Kept {
		if entry.Date.Before(oldest) || entry.Date.Equal(oldest) {
			t.Errorf("the oldest backup survived: %v", entry.Date)
		}
	}
}

func TestPruneKeepsABackupItCannotVerify(t *testing.T) {
	// The rule is the opposite of tidiness: an unverifiable old backup may be the
	// only one that opens, so it stays and the run reports it.
	dir := t.TempDir()
	for day := 1; day <= 3; day++ {
		name := prefix + fixedNow.AddDate(0, 0, -day).Format(nameLayout) + suffix
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not really a backup"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Prune(dir, 1, func(path string) error {
		return fmt.Errorf("integrity_check on %s: not a database", path)
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(result.Kept) != 3 {
		t.Errorf("kept = %d, want all three", len(result.Kept))
	}
	// Kept, but not silently: each one comes back with its reason.
	if len(result.Failed) != 2 {
		t.Errorf("failures = %+v, want the two the keep could not verify", result.Failed)
	}
	for _, entry := range result.Kept {
		if _, err := os.Stat(entry.Path); err != nil {
			t.Errorf("%s was deleted", entry.Name)
		}
	}
}

func TestPruneDeletesTheOldOnesItCanVerify(t *testing.T) {
	dir := t.TempDir()
	for day := 1; day <= 3; day++ {
		name := prefix + fixedNow.AddDate(0, 0, -day).Format(nameLayout) + suffix
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Prune(dir, 1, func(string) error { return nil })
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(result.Kept) != 1 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want the newest only and no failures", result)
	}
	left, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || !left[0].Date.Equal(fixedNow.AddDate(0, 0, -1)) {
		t.Errorf("left = %+v", left)
	}
}

func TestListIgnoresFilesThatAreNotBackups(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harbor-20261122T180000Z.db.zst"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "harbor-broken.db.zst", "harbor-20261122T180000Z.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Bytes != 10 {
		t.Fatalf("entries = %+v, want only the real backup", entries)
	}
}

func TestListOfAMissingDirectoryIsEmpty(t *testing.T) {
	entries, err := List(filepath.Join(t.TempDir(), "gone"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
}

func TestVerifyCatchesACorruptBackup(t *testing.T) {
	snapshotter, dataDir := realSnapshotter(t)
	backups := filepath.Join(dataDir, "backups")
	result, err := Create(context.Background(), snapshotter, backups, clock(fixedNow), DefaultKeep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Truncate the compressed file: not a valid zstd stream any more.
	if err := os.Truncate(result.Kept[0].Path, 20); err != nil {
		t.Fatal(err)
	}
	if err := Verify(result.Kept[0].Path); err == nil {
		t.Fatal("verify accepted a truncated backup")
	}
}

func TestVerifyRejectsAZeroByteBackup(t *testing.T) {
	// An empty zstd stream decompresses to nothing, and SQLite reports
	// integrity_check "ok" for an empty file — so without this check a backup
	// containing nothing would pass as a good one.
	empty := filepath.Join(t.TempDir(), "harbor-20261122T180000Z.db.zst")
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, encoder.EncodeAll(nil, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	encoder.Close()
	if err := Verify(empty); err == nil {
		t.Fatal("verify accepted a zero-byte backup")
	}
}

func TestVerifyRejectsZstdOfSomethingThatIsNotADatabase(t *testing.T) {
	// A valid stream of valid bytes is not a backup. The header check is what
	// separates the two, because integrity_check cannot.
	path := filepath.Join(t.TempDir(), "harbor-20261122T180000Z.db.zst")
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := encoder.EncodeAll([]byte(strings.Repeat("not a database", 100)), nil)
	encoder.Close()
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path); err == nil {
		t.Fatal("verify accepted a compressed text file")
	}
}

func TestATruncatedBackupIsASnapshotFailure(t *testing.T) {
	// Create must not leave a published file when compression or verification
	// fails, or the listing would advertise a backup that cannot be restored.
	_, dataDir := realSnapshotter(t)
	backups := filepath.Join(dataDir, "backups")
	broken := snapFunc(func(context.Context, string) error { return errors.New("disk full") })
	if _, err := Create(context.Background(), broken, backups, clock(fixedNow), DefaultKeep); err == nil {
		t.Fatal("Create with a failing snapshot should error")
	}
	entries, err := List(backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed backup was published: %+v", entries)
	}
}

func TestNamesRoundTrip(t *testing.T) {
	name := prefix + fixedNow.Format(nameLayout) + suffix
	date, ok := parseName(name)
	if !ok || !date.Equal(fixedNow) {
		t.Fatalf("parseName(%q) = %v, %v", name, date, ok)
	}
	for _, bad := range []string{"harbor-.db.zst", "harbor-2026-11-22.db.zst", "backup.db.zst", "harbor-20261122T180000Z.db"} {
		if _, ok := parseName(bad); ok {
			t.Errorf("parseName(%q) accepted a name that is not a backup", bad)
		}
	}
	if !strings.HasPrefix(name, "harbor-") {
		t.Errorf("name = %q", name)
	}
}
