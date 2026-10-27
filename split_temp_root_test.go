package epub

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywltby/go-epub/internal/storage"
	"github.com/ywltby/go-epub/internal/storage/osfs"
)

type splitTempRootSetter interface {
	SetTempRoot(string) error
}

type inspectSplitTempWriter struct {
	root    string
	t       *testing.T
	checked bool
}

func (w *inspectSplitTempWriter) Write(data []byte) (int, error) {
	if !w.checked {
		entries, err := os.ReadDir(w.root)
		if err != nil || len(entries) != 1 || !entries[0].IsDir() {
			w.t.Fatalf("EPUB scratch directory outside owned workspace: %v %v", entries, err)
		}
		w.checked = true
	}
	return len(data), nil
}

func TestSplitTempRootOwnedAndCleaned(t *testing.T) {
	base := t.TempDir()
	previous := filesystem
	filesystem = osfs.NewOSFS(base)
	t.Cleanup(func() { filesystem = previous })
	if err := os.Mkdir(filepath.Join(base, "generation"), 0o700); err != nil {
		t.Fatal(err)
	}
	book, err := NewEpub("book")
	if err != nil {
		t.Fatal(err)
	}
	setter, ok := any(book).(splitTempRootSetter)
	if !ok {
		t.Fatal("EPUB scratch root cannot be bound to generation workspace")
	}
	for _, invalid := range []string{"../other", "/absolute", `..\other`, "generation/../other"} {
		if err := setter.SetTempRoot(invalid); err == nil {
			t.Fatalf("unsafe root accepted: %q", invalid)
		}
	}
	if err := setter.SetTempRoot("generation"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.AddSection("<p>正文</p>", "章节", "chapter.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	out := &inspectSplitTempWriter{root: filepath.Join(base, "generation"), t: t}
	if _, err := book.WriteTo(out); err != nil {
		t.Fatal(err)
	}
	if !out.checked {
		t.Fatal("zip output not written")
	}
	entries, err := os.ReadDir(out.root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch not cleaned: %v %v", entries, err)
	}
}

func TestSplitTempRootCleanupOnWriteFailure(t *testing.T) {
	base := t.TempDir()
	previous := filesystem
	filesystem = osfs.NewOSFS(base)
	t.Cleanup(func() { filesystem = previous })
	if err := os.Mkdir(filepath.Join(base, "generation"), 0o700); err != nil {
		t.Fatal(err)
	}
	book, err := NewEpub("book")
	if err != nil {
		t.Fatal(err)
	}
	setter, ok := any(book).(splitTempRootSetter)
	if !ok {
		t.Fatal("EPUB scratch root cannot be bound to generation workspace")
	}
	if err := setter.SetTempRoot("generation"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.AddSection("<p>正文</p>", "章节", "chapter.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := book.WriteTo(failSplitTempWriter{}); err == nil {
		t.Fatal("failed output succeeded")
	}
	entries, err := os.ReadDir(filepath.Join(base, "generation"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed scratch not cleaned: %v %v", entries, err)
	}
}

type failSplitTempWriter struct{}

func (failSplitTempWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type failSplitCleanupFS struct{ storage.Storage }

func (f failSplitCleanupFS) RemoveAll(string) error { return io.ErrClosedPipe }

func TestSplitTempRootCleanupFailureSurfaced(t *testing.T) {
	base := t.TempDir()
	previous := filesystem
	filesystem = failSplitCleanupFS{osfs.NewOSFS(base)}
	t.Cleanup(func() { filesystem = previous })
	if err := os.Mkdir(filepath.Join(base, "generation"), 0o700); err != nil {
		t.Fatal(err)
	}
	book, err := NewEpub("book")
	if err != nil {
		t.Fatal(err)
	}
	if err := book.SetTempRoot("generation"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.AddSection("<p>body</p>", "chapter", "chapter.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := book.WriteTo(io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("scratch cleanup failure was hidden: %v", err)
	}
}
