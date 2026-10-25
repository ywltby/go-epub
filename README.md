# Shutongwen maintenance fork

Forked from go-shiori/go-epub v1.2.1 (`2f668451ef95fd27bce83491cd85a41f4a4dcf5e`); `main` also tracks later upstream commits through `822e45c`.

- R17: maintain the section filename index in linear space and time.
- R19: propagate write and close errors through every layer.
- MIT copyright remains with the original authors. LICENSE, NOTICE, and the sample licensing notices are preserved.

Module: `github.com/ywltby/go-epub`. Fork prerelease: `v1.2.2-stw.1` (newer than v1.2.1; the suffix identifies the fork).

[![CI](https://github.com/ywltby/go-epub/actions/workflows/ci.yml/badge.svg)](https://github.com/ywltby/go-epub/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/ywltby/go-epub)](https://goreportcard.com/report/github.com/ywltby/go-epub)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/ywltby/go-epub/blob/main/LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/ywltby/go-epub.svg)](https://pkg.go.dev/github.com/ywltby/go-epub)

---

### Features
- [Documented API](https://pkg.go.dev/github.com/ywltby/go-epub)
- Creates valid EPUB 3.0 files
- Adds an additional EPUB 2.0 table of contents for maximum compatibility
- Includes support for adding CSS, images, and fonts

### Contributions

Contributions are welcome; please see [CONTRIBUTING.md](CONTRIBUTING.md) for more information.

### Development

Clone this repository using Git. Run tests as documented below.

Dependencies are managed using [Go modules](https://golang.org/ref/mod)

### Testing

#### EPUBCheck

EPUBCheck is a tool that will check an EPUB for validation errors.

If EPUBCheck is installed locally, it will be run alongside the Go tests. To install EPUBCheck:

1. Make sure you have Java installed on your system

1. Get the latest version of EPUBCheck from [https://github.com/w3c/epubcheck/releases](https://github.com/w3c/epubcheck/releases)

1. Download and extract EPUBCheck in the root directory of this project, e.g.

   ```
   wget https://github.com/IDPF/epubcheck/releases/download/v4.2.5/epubcheck-4.2.5.zip
   unzip epubcheck-4.2.5.zip
   ```

If you do not wish to install EPUBCheck locally, you can manually validate the EPUB:

1. Set `doCleanup = false` in epub_test.go

1. Run the tests (see below)

1. Upload the generated `My EPUB.epub` file to [http://validator.idpf.org/](http://validator.idpf.org/)

#### Run tests

```
go test
```
