package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ywltby/go-epub/internal/storage"
	"github.com/ywltby/go-epub/internal/storage/osfs"
)

func TestReviewRepairR17SectionAllocationGrowth(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic_%v", automatic), func(t *testing.T) {
			var allocated []uint64
			for _, count := range []int{1000, 2000, 4000} {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				e, err := NewEpub("allocation")
				if err != nil {
					t.Fatal(err)
				}
				for n := 0; n < count; n++ {
					name := ""
					if !automatic {
						name = fmt.Sprintf("chapter-%04d.xhtml", n)
					}
					if _, err := e.AddSection("<p>body</p>", "chapter", name, ""); err != nil {
						t.Fatal(err)
					}
				}
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(e)
				allocated = append(allocated, after.TotalAlloc-before.TotalAlloc)
				if len(e.sections) != count {
					t.Fatalf("lost sections: %d want %d", len(e.sections), count)
				}
			}
			t.Logf("allocated bytes for 1000/2000/4000: %v", allocated)
			if allocated[1] > allocated[0]*3 || allocated[2] > allocated[1]*3 {
				t.Fatalf("section insertion is superlinear: %v", allocated)
			}
		})
	}
}

func TestReviewRepairR17SectionNamesAndCoverDeletion(t *testing.T) {
	e, err := NewEpub("index")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddSubSection("missing.xhtml", "body", "missing", "", ""); err == nil {
		t.Fatal("missing parent accepted")
	}
	name, err := e.AddSection("body", "first", "", "")
	if err != nil || name != "section0001.xhtml" {
		t.Fatalf("failed insert reserved automatic name: %s %v", name, err)
	}
	if _, err := e.AddSection("body", "duplicate", "section0001", ""); err == nil {
		t.Fatal("normalized duplicate accepted")
	}
	name, err = e.AddSection("body", "second", "", "")
	if err != nil || name != "section0002.xhtml" {
		t.Fatalf("duplicate insert consumed cursor: %s %v", name, err)
	}
	if _, err := e.AddSubSection("missing.xhtml", "body", "missing", "retry", ""); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err := e.AddSection("body", "retry", "retry", ""); err != nil {
		t.Fatalf("failed insert reserved explicit name: %v", err)
	}

	e, err = NewEpub("cover")
	if err != nil {
		t.Fatal(err)
	}
	image, err := e.AddImage("testdata/gophercolor16x16.png", "first.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetCover(image, ""); err != nil {
		t.Fatal(err)
	}
	child, err := e.AddSubSection(e.cover.xhtmlFilename, "child", "child", "", "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := e.AddSubSection(child, "grandchild", "grandchild", "", "")
	if err != nil {
		t.Fatal(err)
	}
	image, err = e.AddImage("testdata/gophercolor16x16.png", "second.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetCover(image, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddSubSection(child, "orphan", "orphan", "", ""); err == nil {
		t.Fatal("removed subtree remains a valid parent")
	}
	name, err = e.AddSection("reused", "reused", "", "")
	if err != nil || name != child {
		t.Fatalf("removed automatic name not reusable: %s want %s err=%v", name, child, err)
	}
	if _, err := e.AddSection("reused", "reused", grandchild, ""); err != nil {
		t.Fatalf("removed grandchild name occupied: %v", err)
	}
	if _, exists := e.images["first.png"]; exists {
		t.Fatal("replaced cover image still indexed")
	}
}

func TestReviewRepairR17MediaNamesKeepSeparateDirectories(t *testing.T) {
	e, err := NewEpub("domains")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddSection("body", "shared", "shared", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source, folder string
		add                  func(string, string) (string, error)
	}{
		{"css", "testdata/cover.css", CSSFolderName, e.AddCSS},
		{"font", "testdata/redacted-script-regular.ttf", FontFolderName, e.AddFont},
		{"image", "testdata/gophercolor16x16.png", ImageFolderName, e.AddImage},
		{"video", "testdata/sample_640x360.mp4", VideoFolderName, e.AddVideo},
		{"audio", "testdata/sample_audio.wav", AudioFolderName, e.AddAudio},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := tc.add(tc.source, "shared.xhtml")
			if err != nil || path != "../"+tc.folder+"/shared.xhtml" {
				t.Fatalf("independent name rejected: %s %v", path, err)
			}
			var duplicate *FilenameAlreadyUsedError
			if _, err := tc.add(tc.source, "shared.xhtml"); !errors.As(err, &duplicate) {
				t.Fatalf("duplicate error=%v", err)
			}
			if _, err := tc.add("testdata/review-repair-does-not-exist", "retry.xhtml"); err == nil {
				t.Fatal("missing source accepted")
			}
			if _, err := tc.add(tc.source, "retry.xhtml"); err != nil {
				t.Fatalf("failed retrieval reserved filename: %v", err)
			}
		})
	}
}

type reviewRepairFaultFS struct {
	storage.Storage
	target    string
	fault     error
	closeRead bool
	readFault error
	hits      int
}

func (s *reviewRepairFaultFS) WriteFile(name string, body []byte, mode fs.FileMode) error {
	if filepath.Base(name) == s.target && !s.closeRead && s.fault != nil {
		s.hits++
		return s.fault
	}
	return s.Storage.WriteFile(name, body, mode)
}

func (s *reviewRepairFaultFS) Open(name string) (fs.File, error) {
	file, err := s.Storage.Open(name)
	if err != nil || !s.closeRead || s.fault == nil || filepath.Base(name) != s.target {
		return file, err
	}
	return &reviewRepairCloseFile{File: file, owner: s}, nil
}

func (s *reviewRepairFaultFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(s.Storage, name)
}

type reviewRepairCloseFile struct {
	fs.File
	owner *reviewRepairFaultFS
	read  bool
}

func (f *reviewRepairCloseFile) Read(p []byte) (int, error) {
	f.read = true
	if f.owner.readFault != nil {
		return 0, f.owner.readFault
	}
	return f.File.Read(p)
}
func (f *reviewRepairCloseFile) Close() error {
	err := f.File.Close()
	if f.read {
		f.owner.hits++
		return errors.Join(err, f.owner.fault)
	}
	return err
}

func reviewRepairBook(t *testing.T) *Epub {
	t.Helper()
	e, err := NewEpub("review repair")
	if err != nil {
		t.Fatal(err)
	}
	root, err := e.AddSection("<p>first</p>", "first", "chapter.xhtml", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.AddSubSection(root, "<p>second</p>", "second", "child.xhtml", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddSubSection(child, "<p>third</p>", "third", "grandchild.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddSection("<p>last</p>", "last", "last.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestReviewRepairR19FileWriteFailuresAbortAndRetry(t *testing.T) {
	for _, name := range []string{"chapter.xhtml", "child.xhtml", "nav.xhtml", "toc.ncx", "package.opf"} {
		for _, kind := range []string{"EIO", "ENOSPC", "short_write"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				var fault error = syscall.EIO
				if kind == "ENOSPC" {
					fault = syscall.ENOSPC
				}
				if kind == "short_write" {
					fault = io.ErrShortWrite
				}
				staging := t.TempDir()
				injected := &reviewRepairFaultFS{Storage: osfs.NewOSFS(staging), target: name, fault: fault}
				previous := filesystem
				filesystem = injected
				t.Cleanup(func() { filesystem = previous })
				e := reviewRepairBook(t)
				var dst bytes.Buffer
				_, err := e.WriteTo(&dst)
				if injected.hits == 0 {
					t.Fatal("injection did not reach target file")
				}
				if !errors.Is(err, fault) {
					t.Fatalf("write failure was lost: %v", err)
				}
				if dst.Len() != 0 {
					t.Fatal("zipped a failed staging tree")
				}
				if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
					t.Fatalf("failed staging tree remains: entries=%v err=%v", entries, err)
				}
				injected.fault = nil
				dst.Reset()
				if _, err := e.WriteTo(&dst); err != nil {
					t.Fatalf("retry: %v", err)
				}
				reviewRepairCheckArchive(t, dst.Bytes())
			})
		}
	}
}

func TestReviewRepairR19SourceCloseErrorIsReturned(t *testing.T) {
	fault := errors.New("source close EIO")
	injected := &reviewRepairFaultFS{Storage: osfs.NewOSFS(t.TempDir()), target: "child.xhtml", fault: fault, closeRead: true}
	previous := filesystem
	filesystem = injected
	t.Cleanup(func() { filesystem = previous })
	var dst bytes.Buffer
	_, err := reviewRepairBook(t).WriteTo(&dst)
	if injected.hits == 0 {
		t.Fatal("source close injection not exercised")
	}
	if !errors.Is(err, fault) {
		t.Fatalf("source close error lost: %v", err)
	}
}

func TestReviewRepairR19RepeatedWriteKeepsStructure(t *testing.T) {
	previous := filesystem
	filesystem = osfs.NewOSFS(t.TempDir())
	t.Cleanup(func() { filesystem = previous })
	e := reviewRepairBook(t)
	for attempt := 0; attempt < 2; attempt++ {
		var dst bytes.Buffer
		if _, err := e.WriteTo(&dst); err != nil {
			t.Fatal(err)
		}
		reviewRepairCheckArchive(t, dst.Bytes())
	}
}

type reviewRepairZipWriter struct {
	bytes.Buffer
	mode      string
	fault     error
	atCentral bool
}

func (w *reviewRepairZipWriter) Write(p []byte) (int, error) {
	w.atCentral = w.atCentral || bytes.Contains(p, []byte{'P', 'K', 1, 2})
	if w.mode == "central_directory" && !bytes.Contains(p, []byte{'P', 'K', 1, 2}) {
		return w.Buffer.Write(p)
	}
	if w.mode == "short_write" {
		return w.Buffer.Write(p[:len(p)-1])
	}
	return 0, w.fault
}

func TestReviewRepairR19ZipWriteAndCloseFailures(t *testing.T) {
	for _, mode := range []string{"write", "short_write", "central_directory"} {
		t.Run(mode, func(t *testing.T) {
			previous := filesystem
			filesystem = osfs.NewOSFS(t.TempDir())
			t.Cleanup(func() { filesystem = previous })
			fault := errors.New("zip EIO")
			writer := &reviewRepairZipWriter{mode: mode, fault: fault}
			e := reviewRepairBook(t)
			if mode != "central_directory" {
				body := make([]byte, 1<<20)
				random := rand.New(rand.NewSource(1))
				for i := range body {
					body[i] = byte('a' + random.Intn(26))
				}
				if _, err := e.AddSection(string(body), "large", "large.xhtml", ""); err != nil {
					t.Fatal(err)
				}
			}
			n, err := e.WriteTo(writer)
			if mode == "short_write" {
				if !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("short write error=%v", err)
				}
			} else if !errors.Is(err, fault) {
				t.Fatalf("zip error=%v", err)
			}
			if n != int64(writer.Len()) {
				t.Fatalf("reported %d bytes, actually wrote %d", n, writer.Len())
			}
			if writer.atCentral != (mode == "central_directory") {
				t.Fatalf("fault did not exercise intended write/close phase: central=%v", writer.atCentral)
			}
		})
	}
}

func TestReviewRepairR19ReadAndCloseErrorsPreserveBothCauses(t *testing.T) {
	readFault, closeFault := errors.New("read EIO"), errors.New("close EIO")
	previous := filesystem
	injected := &reviewRepairFaultFS{Storage: osfs.NewOSFS(t.TempDir()), target: "child.xhtml", fault: closeFault, closeRead: true, readFault: readFault}
	filesystem = injected
	t.Cleanup(func() { filesystem = previous })
	_, err := reviewRepairBook(t).WriteTo(io.Discard)
	if injected.hits == 0 || !errors.Is(err, readFault) || !errors.Is(err, closeFault) {
		t.Fatalf("read/close causes not preserved: hits=%d err=%v", injected.hits, err)
	}
}

func TestReviewRepairR19WritePreservesCreateCause(t *testing.T) {
	err := reviewRepairBook(t).Write(filepath.Join(t.TempDir(), "missing", "book.epub"))
	var pathError *fs.PathError
	if !errors.As(err, &pathError) {
		t.Fatalf("destination create cause lost: %v", err)
	}
}

func BenchmarkReviewRepairR17Sections(b *testing.B) {
	for _, automatic := range []bool{false, true} {
		for _, count := range []int{1000, 2000, 4000} {
			b.Run(fmt.Sprintf("automatic_%v/%d", automatic, count), func(b *testing.B) {
				b.ReportAllocs()
				for iteration := 0; iteration < b.N; iteration++ {
					e, err := NewEpub("benchmark")
					if err != nil {
						b.Fatal(err)
					}
					for n := 0; n < count; n++ {
						name := ""
						if !automatic {
							name = fmt.Sprintf("chapter-%04d.xhtml", n)
						}
						if _, err := e.AddSection("<p>body</p>", "chapter", name, ""); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func reviewRepairCheckArchive(t *testing.T, raw []byte) {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range z.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if closeErr := reader.Close(); err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
		files[entry.Name] = body
	}
	names := []string{"chapter.xhtml", "child.xhtml", "grandchild.xhtml", "last.xhtml"}
	var pkgDoc struct {
		Items []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			ID string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(files["EPUB/package.opf"], &pkgDoc); err != nil {
		t.Fatal(err)
	}
	var spine []string
	manifest := map[string]string{}
	for _, item := range pkgDoc.Items {
		if _, exists := manifest[item.ID]; exists {
			t.Fatalf("duplicate manifest ID: %s", item.ID)
		}
		manifest[item.ID] = item.Href
		if _, exists := files["EPUB/"+item.Href]; !exists {
			t.Fatalf("missing manifest file: %s", item.Href)
		}
	}
	for _, item := range pkgDoc.Spine {
		spine = append(spine, item.ID)
	}
	if !reflect.DeepEqual(spine, names) {
		t.Fatalf("spine=%v want=%v", spine, names)
	}
	for _, name := range names {
		if manifest[name] != "xhtml/"+name {
			t.Fatalf("missing section manifest entry: %s", name)
		}
	}
	for _, tocName := range []string{"EPUB/nav.xhtml", "EPUB/toc.ncx"} {
		decoder := xml.NewDecoder(bytes.NewReader(files[tocName]))
		var order []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "a" && start.Name.Local != "content" {
				continue
			}
			for _, attr := range start.Attr {
				if attr.Name.Local == "href" || attr.Name.Local == "src" {
					order = append(order, strings.TrimPrefix(attr.Value, "xhtml/"))
				}
			}
		}
		if !reflect.DeepEqual(order, names) {
			t.Fatalf("%s order=%v want=%v", tocName, order, names)
		}
	}
}
