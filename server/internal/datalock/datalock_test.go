package datalock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The lock is the only thing standing between a restore and a live connection,
// so its two interesting properties are tested: it excludes a second holder,
// and it comes back after release.

func TestAcquireExcludesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release()

	second, err := Acquire(dir)
	if err == nil {
		_ = second.Release()
		t.Fatal("a second Acquire succeeded while the first was held")
	}
	if !errors.Is(err, ErrHeld) {
		t.Errorf("err = %v, want ErrHeld", err)
	}
}

func TestReleaseLetsTheNextHolderIn(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireCreatesTheDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	held, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release()
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Errorf("nil Release: %v", err)
	}
}
