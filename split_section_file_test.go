package epub

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ywltby/go-epub/internal/storage/osfs"
)

type splitSectionFileAdder interface {
	AddSectionFile(path, title, filename, cssPath string) (string, error)
}

func splitAdder(t *testing.T, book *Epub) splitSectionFileAdder {
	t.Helper()
	adder, ok := any(book).(splitSectionFileAdder)
	if !ok {
		t.Fatal("EPUB cannot add chapter body from a disk file")
	}
	return adder
}

func TestSplitSectionFileMatchesInline(t *testing.T) {
	previous := filesystem
	filesystem = osfs.NewOSFS(t.TempDir())
	t.Cleanup(func() { filesystem = previous })
	body := "<h1>第一章 &amp; A</h1><p>正文<br/>\n<img src=\"../images/x.png\" alt=\"图片\"/></p>"
	path := filepath.Join(t.TempDir(), "body.xhtml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	inline, err := NewEpub("书名")
	if err != nil {
		t.Fatal(err)
	}
	spooled, err := NewEpub("书名")
	if err != nil {
		t.Fatal(err)
	}
	inline.SetIdentifier("same")
	spooled.SetIdentifier("same")
	if _, err := inline.AddSection(body, "一章", "chapter-000001.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := splitAdder(t, spooled).AddSectionFile(path, "一章", "chapter-000001.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	readEntry := func(book *Epub, name string) []byte {
		t.Helper()
		var output bytes.Buffer
		if _, err := book.WriteTo(&output); err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range archive.File {
			if entry.Name == name {
				r, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				closeErr := r.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
				return data
			}
		}
		t.Fatalf("missing archive entry %s", name)
		return nil
	}
	for _, entry := range []string{"EPUB/xhtml/chapter-000001.xhtml", "EPUB/nav.xhtml", "EPUB/toc.ncx"} {
		if a, b := readEntry(inline, entry), readEntry(spooled, entry); !bytes.Equal(a, b) {
			t.Errorf("entry %s differs:\ninline=%s\nfile=%s", entry, a, b)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := spooled.WriteTo(io.Discard); err == nil {
		t.Fatal("missing spool file did not fail")
	}
}

func TestSplitSectionFileDoesNotRetainBodies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.xhtml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 64*1024)
	for range 128 {
		if _, err := io.WriteString(f, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	book, err := NewEpub("book")
	if err != nil {
		t.Fatal(err)
	}
	adder := splitAdder(t, book)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range 32 {
		if _, err := adder.AddSectionFile(path, fmt.Sprintf("chapter %d", i), fmt.Sprintf("chapter-%06d.xhtml", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(book)
	if after.HeapAlloc > before.HeapAlloc+16*1024*1024 {
		t.Fatalf("section bodies retained in heap: before=%d after=%d", before.HeapAlloc, after.HeapAlloc)
	}
}
