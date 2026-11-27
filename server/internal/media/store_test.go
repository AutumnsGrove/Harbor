package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestPutIsContentAddressed is the central promise: the name is the bytes.
func TestPutIsContentAddressed(t *testing.T) {
	s := newStore(t)
	body := []byte("a photograph, allegedly")
	blob, err := s.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if blob.SHA != want {
		t.Errorf("SHA = %s, want %s (sha256sum)", blob.SHA, want)
	}
	if blob.Path != want[:2]+"/"+want {
		t.Errorf("path = %q, want the fanned-out hash", blob.Path)
	}
	if blob.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", blob.Bytes, len(body))
	}
	if blob.Existed {
		t.Error("a new blob reported Existed")
	}

	// The file on disk is the bytes, and its own sha256 agrees.
	onDisk, err := os.ReadFile(filepath.Join(s.Root(), filepath.FromSlash(blob.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Errorf("stored bytes differ from what was written")
	}
	again := sha256.Sum256(onDisk)
	if hex.EncodeToString(again[:]) != blob.SHA {
		t.Error("the stored file's own hash does not match its name")
	}
}

// The same bytes twice is one file and the second call says so: this is what
// lets the pipeline skip a second vision call (plans/06-media test 2).
func TestTheSameBytesAreStoredOnce(t *testing.T) {
	s := newStore(t)
	body := []byte("the same photo from Telegram and from the composer")
	first, err := s.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA != first.SHA {
		t.Fatalf("two hashes for one blob: %s and %s", first.SHA, second.SHA)
	}
	if !second.Existed {
		t.Error("the second write did not report that the blob was already there")
	}

	files := countFiles(t, s.Root())
	if files != 1 {
		t.Errorf("%d files on disk, want 1", files)
	}
	// A reused blob must not leave debris in the incoming directory.
	if debris := countFiles(t, filepath.Join(s.Root(), ".incoming")); debris != 0 {
		t.Errorf("%d temp files left behind", debris)
	}
}

// An empty body is a legitimate blob: a save with no bytes still has an
// identity, and refusing it here would turn a UI bug into a failed save.
func TestEmptyBlobIsStored(t *testing.T) {
	s := newStore(t)
	blob, err := s.Put(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Put(empty): %v", err)
	}
	sum := sha256.Sum256(nil)
	if blob.SHA != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA = %s, want the hash of nothing", blob.SHA)
	}
	if ok, err := s.Has(blob.SHA); err != nil || !ok {
		t.Errorf("Has = %v, %v; want true", ok, err)
	}
}

// The cap is enforced while streaming, so a body of unknown length cannot be
// used to fill the disk.
func TestOverTheCapIsRefusedAndLeavesNothing(t *testing.T) {
	s := newStore(t).WithMaxBytes(64)
	if _, err := s.Put(bytes.NewReader(bytes.Repeat([]byte("x"), 65))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put(65 bytes) = %v, want ErrTooLarge", err)
	}
	if files := countFiles(t, s.Root()); files != 0 {
		t.Errorf("%d files after a refused put, want 0", files)
	}
	// Exactly at the cap is allowed: the boundary is where a cap gets tested.
	if _, err := s.Put(bytes.NewReader(bytes.Repeat([]byte("y"), 64))); err != nil {
		t.Errorf("Put(exactly the cap) = %v", err)
	}
}

// A reader that fails mid-stream must not leave a partial file that a later
// lookup would then trust.
func TestAFailedReadLeavesNothing(t *testing.T) {
	s := newStore(t)
	failing := io.MultiReader(
		strings.NewReader("the beginning of a photo"),
		iotest.ErrReader(errors.New("connection reset")),
	)
	if _, err := s.Put(failing); err == nil {
		t.Fatal("Put with a failing reader reported success")
	}
	if files := countFiles(t, s.Root()); files != 0 {
		t.Errorf("%d files after a failed put, want 0", files)
	}
}

// Opening and Has round-trip, and a hash that is not a hash never reaches the
// filesystem.
func TestOpenAndHas(t *testing.T) {
	s := newStore(t)
	body := []byte("open me")
	blob, err := s.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := s.Open(blob.SHA)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Open gave %q", got)
	}
	if ok, err := s.Has(blob.SHA); err != nil || !ok {
		t.Errorf("Has = %v, %v", ok, err)
	}
	missing := strings.Repeat("0", 64)
	if ok, err := s.Has(missing); err != nil || ok {
		t.Errorf("Has(missing) = %v, %v; want false, nil", ok, err)
	}
	if _, err := s.Open(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open(missing) = %v, want ErrNotExist", err)
	}
}

// Nothing but 64 lowercase hex characters becomes a path (plans/06-media test 1).
func TestOnlyHashesBecomePaths(t *testing.T) {
	s := newStore(t)
	bad := []string{
		"", "../etc/passwd", "ab/../../etc/passwd", strings.Repeat("a", 63),
		strings.Repeat("a", 65), strings.ToUpper(strings.Repeat("a", 64)),
		strings.Repeat("g", 64), strings.Repeat("a", 64) + "\x00",
		filepath.Join("a", strings.Repeat("b", 64)),
	}
	for _, sha := range bad {
		if _, err := s.Open(sha); !errors.Is(err, ErrBadHash) {
			t.Errorf("Open(%q) = %v, want ErrBadHash", sha, err)
		}
		if _, err := s.Open(sha); err == nil {
			t.Errorf("Open(%q) succeeded", sha)
		}
		if abs, err := s.Abs(sha); err == nil && !strings.HasPrefix(abs, s.Root()) {
			t.Errorf("Abs(%q) = %q, which escapes the store", sha, abs)
		}
	}
	// A well-formed hash does become a path.
	good := strings.Repeat("ab", 32)
	abs, err := s.Abs(good)
	if err != nil {
		t.Fatalf("Abs(good) = %v", err)
	}
	if want := filepath.Join(s.Root(), "ab", good); abs != want {
		t.Errorf("Abs = %q, want %q", abs, want)
	}
}

// FuzzPathFor keeps the promise that a hash-shaped string cannot escape the
// store, whatever it contains.
func FuzzStorePaths(f *testing.F) {
	f.Add(strings.Repeat("ab", 32))
	f.Add("../../etc/passwd")
	f.Add("")
	f.Add(strings.Repeat("\x00", 64))
	dir := f.TempDir()
	s, err := New(filepath.Join(dir, "media"))
	if err != nil {
		f.Fatal(err)
	}
	root := s.Root()
	f.Fuzz(func(t *testing.T, sha string) {
		abs, err := s.Abs(sha)
		if err != nil {
			return // refused is always acceptable
		}
		rel, relErr := filepath.Rel(root, abs)
		if relErr != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			t.Fatalf("Abs(%q) = %q escapes %q", sha, abs, root)
		}
		if !IsHash(sha) {
			t.Fatalf("Abs accepted %q, which is not a hash", sha)
		}
	})
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return n
}

// TestPutStreamsRatherThanBuffers is plans/06-media test 3, done the way the
// plan asks: a reader that records how many bytes it was asked for at once. A
// buffering implementation would ask for the whole blob; this one asks for one
// copy buffer at a time, which is what keeps a 25 MB photo off the heap.
func TestPutStreamsRatherThanBuffers(t *testing.T) {
	s := newStore(t)
	const size = 8 << 20
	largest := 0
	reads := 0
	src := &trackingReader{
		remaining: size,
		onRead: func(n int) {
			reads++
			if n > largest {
				largest = n
			}
		},
	}
	blob, err := s.Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if blob.Bytes != size {
		t.Errorf("bytes = %d, want %d", blob.Bytes, size)
	}
	if largest > copyBuffer {
		t.Errorf("one Read asked for %d bytes; the copy buffer is %d, so the blob was not streamed",
			largest, copyBuffer)
	}
	if reads < size/copyBuffer/2 {
		t.Errorf("only %d reads for %d bytes: it did not stream in chunks", reads, size)
	}
}

type trackingReader struct {
	remaining int
	onRead    func(int)
}

func (r *trackingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = byte(i)
	}
	r.remaining -= n
	r.onRead(n)
	return n, nil
}
