package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-shiori/go-epub/internal/storage"
	"github.com/go-shiori/go-epub/internal/storage/osfs"
)

func TestReviewRepairR19CrossSecondRetryKeepsSingleModifiedMetadata(t *testing.T) {
	previous := filesystem
	filesystem = osfs.NewOSFS(t.TempDir())
	t.Cleanup(func() { filesystem = previous })
	e, err := NewEpub("metadata retry")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chapter.xhtml", "child.xhtml", "grandchild.xhtml", "last.xhtml"} {
		if _, err := e.AddSection("<p>body</p>", name, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	e.SetAuthor("retained author")
	for attempt := 0; attempt < 2; attempt++ {
		var out bytes.Buffer
		if _, err := e.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var modified []string
		for _, file := range z.File {
			if file.Name != "EPUB/package.opf" {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			var pkgDoc struct {
				Metadata struct {
					Creator string `xml:"creator"`
					Meta    []struct {
						Property string `xml:"property,attr"`
						Text     string `xml:",chardata"`
					} `xml:"meta"`
				} `xml:"metadata"`
			}
			err = xml.NewDecoder(reader).Decode(&pkgDoc)
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				t.Fatal(errors.Join(err, closeErr))
			}
			if pkgDoc.Metadata.Creator != "retained author" {
				t.Fatal("retry lost unrelated author metadata")
			}
			for _, meta := range pkgDoc.Metadata.Meta {
				if meta.Property == "dcterms:modified" {
					modified = append(modified, meta.Text)
				}
			}
		}
		if len(modified) != 1 {
			t.Errorf("attempt=%d modified metadata count=%d values=%v", attempt, len(modified), modified)
		}
		reviewRepairCheckArchive(t, out.Bytes())
		if attempt == 0 {
			time.Sleep(1100 * time.Millisecond)
		}
	}
}

type reviewR19MediaCase struct {
	name, source, filename string
	add                    func(*Epub, string, string) (string, error)
}

func reviewR19MediaCases() []reviewR19MediaCase {
	return []reviewR19MediaCase{
		{"css", "cover.css", "asset.css", (*Epub).AddCSS},
		{"font", "redacted-script-regular.ttf", "asset.ttf", (*Epub).AddFont},
		{"image", "gophercolor16x16.png", "asset.png", (*Epub).AddImage},
		{"video", "sample_640x360.mp4", "asset.mp4", (*Epub).AddVideo},
		{"audio", "sample_audio.wav", "asset.wav", (*Epub).AddAudio},
	}
}

type reviewR19MediaFS struct {
	storage.Storage
	target                                       string
	createErr, writeErr, closeErr, sniffCloseErr error
	creates, closes, sniffCloses                 int
}

func (s *reviewR19MediaFS) Create(name string) (storage.File, error) {
	if filepath.Base(name) != s.target {
		return s.Storage.Create(name)
	}
	s.creates++
	if s.createErr != nil {
		return nil, s.createErr
	}
	file, err := s.Storage.Create(name)
	if err != nil {
		return nil, err
	}
	return &reviewR19MediaFile{File: file, owner: s}, nil
}

func (s *reviewR19MediaFS) Open(name string) (fs.File, error) {
	file, err := s.Storage.Open(name)
	if err != nil || filepath.Base(name) != s.target || s.sniffCloseErr == nil {
		return file, err
	}
	return &reviewR19SniffFile{File: file, owner: s}, nil
}

type reviewR19MediaFile struct {
	storage.File
	owner *reviewR19MediaFS
}

func (f *reviewR19MediaFile) Write(p []byte) (int, error) {
	if f.owner.writeErr != nil {
		return 0, f.owner.writeErr
	}
	return f.File.Write(p)
}

func (f *reviewR19MediaFile) Close() error {
	f.owner.closes++
	return errors.Join(f.File.Close(), f.owner.closeErr)
}

type reviewR19SniffFile struct {
	fs.File
	owner *reviewR19MediaFS
	read  bool
}

func (f *reviewR19SniffFile) Read(p []byte) (int, error) {
	f.read = true
	return f.File.Read(p)
}

func (f *reviewR19SniffFile) Close() error {
	err := f.File.Close()
	if f.read {
		f.owner.sniffCloses++
		return errors.Join(err, f.owner.sniffCloseErr)
	}
	return err
}

func TestReviewRepairR19MediaDestinationFailuresAbortAndRetry(t *testing.T) {
	for _, media := range reviewR19MediaCases() {
		for _, kind := range []string{"close", "write_and_close", "create"} {
			t.Run(media.name+"/"+kind, func(t *testing.T) {
				closeErr, writeErr, createErr := errors.New("media close EIO"), errors.New("media write ENOSPC"), errors.New("media create EIO")
				staging := t.TempDir()
				injected := &reviewR19MediaFS{Storage: osfs.NewOSFS(staging), target: media.filename, closeErr: closeErr}
				if kind == "create" {
					injected.createErr = createErr
				}
				if kind == "write_and_close" {
					injected.writeErr = writeErr
				}
				wantCauses := []error{closeErr}
				wantCloses := 1
				if kind == "create" {
					wantCauses = []error{createErr}
					wantCloses = 0
				} else if kind == "write_and_close" {
					wantCauses = append(wantCauses, writeErr)
				}
				previous := filesystem
				filesystem = injected
				t.Cleanup(func() { filesystem = previous })
				e := reviewRepairBook(t)
				if _, err := media.add(e, filepath.Join("testdata", media.source), media.filename); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				_, err := e.WriteTo(&out)
				for _, want := range wantCauses {
					if !errors.Is(err, want) {
						t.Errorf("media destination error cause lost: want=%v err=%v", want, err)
					}
				}
				if injected.creates != 1 || injected.closes != wantCloses {
					t.Fatalf("fault path not exercised exactly once: create=%d close=%d", injected.creates, injected.closes)
				}
				if out.Len() != 0 {
					t.Fatal("zipped a failed media staging tree")
				}
				if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
					t.Fatalf("failed media staging tree remains: %v %v", entries, err)
				}
				injected.createErr, injected.writeErr, injected.closeErr = nil, nil, nil
				out.Reset()
				if _, err := e.WriteTo(&out); err != nil {
					t.Fatalf("media retry: %v", err)
				}
				reviewRepairCheckArchive(t, out.Bytes())
			})
		}
	}
}

type reviewR19SourceBody struct {
	io.Reader
	readErr, closeErr error
	closes            *int
}

func (r *reviewR19SourceBody) Read(p []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.Reader.Read(p)
}

func (r *reviewR19SourceBody) Close() error {
	*r.closes++
	return r.closeErr
}

type reviewR19Transport func(*http.Request) (*http.Response, error)

func (f reviewR19Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewRepairR19MediaSourceFailuresAbort(t *testing.T) {
	for _, media := range reviewR19MediaCases() {
		for _, kind := range []string{"close", "read_and_close", "http_error_and_close"} {
			t.Run(media.name+"/"+kind, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join("testdata", media.source))
				if err != nil {
					t.Fatal(err)
				}
				closeErr, readErr := errors.New("source close EIO"), errors.New("source read EIO")
				previous := filesystem
				filesystem = osfs.NewOSFS(t.TempDir())
				t.Cleanup(func() { filesystem = previous })
				e := reviewRepairBook(t)
				closes := 0
				e.Client = &http.Client{Transport: reviewR19Transport(func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodHead {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}, Request: req}, nil
					}
					body := &reviewR19SourceBody{Reader: bytes.NewReader(data), closeErr: closeErr, closes: &closes}
					code := 200
					if kind == "read_and_close" {
						body.readErr = readErr
					}
					if kind == "http_error_and_close" {
						code = 503
					}
					return &http.Response{StatusCode: code, Body: body, Header: http.Header{}, Request: req}, nil
				})}
				if _, err := media.add(e, "https://media.invalid/"+media.filename, media.filename); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				_, err = e.WriteTo(&out)
				if closes != 1 || !errors.Is(err, closeErr) {
					t.Errorf("media source close failure lost: closes=%d err=%v", closes, err)
				}
				if kind == "read_and_close" && !errors.Is(err, readErr) {
					t.Errorf("media source read failure lost: %v", err)
				}
				if out.Len() != 0 {
					t.Fatal("zipped a failed source staging tree")
				}
			})
		}
	}
}

func TestReviewRepairR19MediaAdmissionCloseFailuresDoNotReserveNames(t *testing.T) {
	for _, media := range reviewR19MediaCases() {
		for _, code := range []int{200, 503} {
			t.Run(media.name+"/"+http.StatusText(code), func(t *testing.T) {
				e := reviewRepairBook(t)
				closeErr := errors.New("admission body close EIO")
				closes := 0
				e.Client = &http.Client{Transport: reviewR19Transport(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: code, Body: &reviewR19SourceBody{Reader: bytes.NewReader(nil), closeErr: closeErr, closes: &closes}, Header: http.Header{}, Request: req}, nil
				})}
				if _, err := media.add(e, "https://media.invalid/"+media.filename, media.filename); closes != 1 || !errors.Is(err, closeErr) {
					t.Fatalf("admission close error lost or body leaked: closes=%d err=%v", closes, err)
				}
				if _, err := media.add(e, filepath.Join("testdata", media.source), media.filename); err != nil {
					t.Fatalf("failed admission reserved media filename: %v", err)
				}
			})
		}
	}
}

func TestReviewRepairR19MediaSniffCloseFailureAbortsBeforeZIP(t *testing.T) {
	for _, media := range reviewR19MediaCases() {
		t.Run(media.name, func(t *testing.T) {
			closeErr := errors.New("sniff close EIO")
			injected := &reviewR19MediaFS{Storage: osfs.NewOSFS(t.TempDir()), target: media.filename, sniffCloseErr: closeErr}
			previous := filesystem
			filesystem = injected
			t.Cleanup(func() { filesystem = previous })
			e := reviewRepairBook(t)
			if _, err := media.add(e, filepath.Join("testdata", media.source), media.filename); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err := e.WriteTo(&out)
			if !errors.Is(err, closeErr) || injected.sniffCloses != 1 || out.Len() != 0 {
				t.Fatalf("sniff close must abort staging: closes=%d zip_bytes=%d err=%v", injected.sniffCloses, out.Len(), err)
			}
		})
	}
}

func TestReviewRepairR19MediaWriteAndBothCloseCausesArePreserved(t *testing.T) {
	targetClose, sourceClose, writeErr := errors.New("target close EIO"), errors.New("source close EIO"), errors.New("target write ENOSPC")
	injected := &reviewR19MediaFS{Storage: osfs.NewOSFS(t.TempDir()), target: "asset.png", writeErr: writeErr, closeErr: targetClose}
	previous := filesystem
	filesystem = injected
	t.Cleanup(func() { filesystem = previous })
	e := reviewRepairBook(t)
	data, err := os.ReadFile("testdata/gophercolor16x16.png")
	if err != nil {
		t.Fatal(err)
	}
	closes := 0
	e.Client = &http.Client{Transport: reviewR19Transport(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodHead {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}, Request: req}, nil
		}
		return &http.Response{StatusCode: 200, Body: &reviewR19SourceBody{Reader: bytes.NewReader(data), closeErr: sourceClose, closes: &closes}, Header: http.Header{}, Request: req}, nil
	})}
	if _, err := e.AddImage("https://media.invalid/asset.png", "asset.png"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = e.WriteTo(&out)
	for _, cause := range []error{writeErr, targetClose, sourceClose} {
		if !errors.Is(err, cause) {
			t.Errorf("media cause lost: %v err=%v", cause, err)
		}
	}
	if closes != 1 || injected.closes != 1 || out.Len() != 0 {
		t.Fatalf("media close lifecycle mismatch: source=%d target=%d zip_bytes=%d", closes, injected.closes, out.Len())
	}
}
