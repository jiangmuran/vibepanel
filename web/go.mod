// Not a Go module anybody builds. It exists to keep `./...` out of here.
//
// npm packages ship whatever they like, and one of them ships Go: flatted
// carries golang/pkg/flatted/flatted.go, so `go build ./...`, `go vet ./...`
// and `go test ./...` were all compiling and checking a third-party file that
// arrives and changes with `npm ci`. `go test -cover ./...` listed it as a
// package of this project.
//
// Go has no exclude directive; a nested go.mod is the mechanism. Everything
// below this file belongs to another module and the root's `./...` stops at
// it. Nothing here is imported, built or published.
//
// The Makefile's gofmt line already filtered `^web/` — the same symptom,
// noticed once and patched in one command out of four.

module github.com/jiangmuran/vibepanel/web

go 1.26
