package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
)

// Rendering a project's HTML is handing a browser a document somebody else
// wrote. This file is the isolation that makes that acceptable, and the
// reasoning belongs next to the code rather than in a design note nobody opens.
//
// The thing being defended: the panel's origin holds a session cookie that is a
// writable terminal. A page from a project directory getting script access to
// that origin is not "an XSS", it is a shell. That is why SniffMagic refuses
// SVG and why the ordinary preview endpoint answers with an attachment
// disposition -- and both of those stay exactly as they are. This is a second,
// narrower door.
//
// # The design
//
// Four layers, and each is written down with what it does *not* buy.
//
//  1. **A separate route.** Same shape as red line 8: the capability "these
//     bytes are rendered as a document" is narrowed by which handler answers,
//     not by a query parameter on the handler that already exists. GET
//     /preview stays exactly as it was -- attachment, octet-stream, nosniff --
//     so nothing that reaches it can be talked into rendering. This buys
//     reviewability, not protection: it is what makes "which code path can
//     produce an inline text/html response" a question with one answer.
//
//  2. **An iframe sandbox with no allow-same-origin.** This is the actual
//     boundary. A sandboxed document gets an opaque origin, so document.cookie
//     is empty, storage throws, and window.parent is cross-origin and
//     unreadable. allow-same-origin is never emitted, in either direction --
//     with allow-scripts beside it the frame could reach into the parent
//     document and the whole thing would be theatre. previewSandbox is the only
//     place the value is built, and a test asserts the two never appear
//     together. What it does not buy: it does not stop the document *drawing*
//     anything it likes, and it does not stop a click inside it navigating the
//     frame.
//
//  3. **A Content-Security-Policy on the response.** default-src 'none' is the
//     part that keeps the panel's "does not phone home" promise honest: a
//     preview containing <img src="https://someone/?leak"> or a nested iframe
//     would otherwise be an outbound request nobody asked for, made by the
//     panel's own browser tab, the moment a file was clicked. Only data: and
//     blob: subresources load, and neither touches a network. The policy also
//     carries the sandbox *directive*, which matters more than it looks:
//     the iframe attribute applies only when the panel is doing the framing,
//     and this URL can be typed into a tab. The header applies either way.
//     What it does not buy: it does not restrict the frame navigating itself
//     when a person clicks a link (navigate-to was removed from the spec), and
//     it does not help against a bug in the browser's own parser.
//
//  4. **Scripts are off unless somebody asks, and the server decides.** The
//     effective sandbox is the intersection of the attribute and the header, so
//     the browser runs script only when *both* say so. Editing the sandbox
//     attribute in devtools therefore gets you nothing; the decision lives in
//     previewCSP, on this side. What it does not buy: with scripts on, a page
//     can still burn a core in an opaque origin until the dialog is closed.
//
// # What an attacker can still do
//
// Written out because a threat model with no residue is one nobody checked.
//
//   - Draw anything. A preview can look like a sign-in form. It cannot submit
//     one (form-action 'none', and allow-forms is not granted), cannot open a
//     window (no allow-popups), cannot navigate the tab (no allow-top-
//     navigation) and cannot reach a network. It is a picture of a lie inside a
//     dialog whose header shows the file's real name.
//   - Navigate itself on a click. A link in the preview can take the *frame* to
//     a remote page, which is a network request -- a person-initiated one, which
//     is the line the product draws. The document that lands is in the same
//     sandbox and gets nothing.
//   - Hang, with scripts explicitly enabled. Closing the dialog removes the
//     frame and ends it.
//   - postMessage at the parent. The panel registers no message listener; if
//     one is ever added it must check the origin, which for this frame is the
//     string "null".
//   - Exploit the renderer. A sandbox is not a chroot. Out of scope here in the
//     same way it is out of scope for every other page the browser loads.

// previewSandbox is the iframe sandbox attribute, and the CSP sandbox directive
// is built from the same answer.
//
// One function so the two can never disagree, and so the property that matters
// -- allow-same-origin is not in it -- is testable rather than spread over a
// component and a handler.
//
// The tokens deliberately absent, each of which somebody will suggest adding:
// allow-same-origin (hands over the panel's origin), allow-popups and
// allow-popups-to-escape-sandbox (a preview that opens windows), allow-modals
// (an alert() over the whole panel from a file), allow-top-navigation and its
// -by-user-activation variant (a file that redirects the tab), allow-downloads
// (a preview that starts a save), allow-forms (a form that posts somewhere).
func previewSandbox(scripts bool) string {
	if scripts {
		return "allow-scripts"
	}
	return ""
}

// previewCSP is the policy served with a rendered preview.
//
// style-src 'unsafe-inline' is the one concession, and it is unavoidable: a
// page's own <style> block and its style= attributes are the whole of how it
// looks, and an external stylesheet is a network fetch this refuses anyway.
// script-src 'unsafe-inline' appears only alongside allow-scripts, and
// 'unsafe-eval' deliberately does not -- a bundle that needs eval renders
// wrong, which is a smaller cost than the alternative.
func previewCSP(scripts bool) string {
	// frame-ancestors 'self' rather than the 'none' the global middleware sets:
	// the panel is the thing framing this, and 'none' means the iframe loads a
	// blank box. Set here, after securityHeaders has run, so this is an
	// override rather than a hole -- every other response keeps 'none'.
	policy := "default-src 'none'; img-src data: blob:; media-src data: blob:; " +
		"font-src data:; style-src 'unsafe-inline'; base-uri 'none'; " +
		"form-action 'none'; frame-ancestors 'self'"
	if scripts {
		policy += "; script-src 'unsafe-inline'; sandbox allow-scripts"
		return policy
	}
	return policy + "; sandbox"
}

// handleRenderPreview serves one markup file as a document, isolated.
//
// The containment is the same browse.Resolve every other file route uses --
// there is one path check in this codebase and this is not a second one.
func (s *Server) handleRenderPreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	abs, err := browse.Resolve(p.Path, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseErr(w, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "that is a directory")
		return
	}
	// Regular files only, for the reason spelled out on handleDownload: opening
	// a FIFO with no writer never returns, and it takes the request goroutine
	// and graceful shutdown with it.
	if !info.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, "not a regular file")
		return
	}
	if info.Size() > previewMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "too large to render")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, http.StatusForbidden, "cannot read that file")
		return
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	name := filepath.Base(abs)

	// The whitelist, and the only place a project's bytes can acquire a
	// renderable content type. Anything that is not markup is refused outright
	// rather than falling back to text: the caller already has an endpoint that
	// serves text, and a route that quietly serves something else is how "which
	// handler can render" stops having one answer.
	kind, mime := browse.SniffMarkup(name, head)
	if kind == browse.MarkupNone {
		writeErr(w, http.StatusUnsupportedMediaType, "not a page")
		return
	}

	// Exactly "1" enables scripts. Not strconv.ParseBool: this is the switch
	// between "cannot execute" and "can", and it should be reachable by one
	// spelling rather than by six.
	scripts := r.URL.Query().Get("scripts") == "1"

	h := w.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Security-Policy", previewCSP(scripts))
	// nosniff as well as an explicit type. Without it a browser that disagrees
	// about the bytes can promote them to something else, and "something else"
	// here is chosen by the file.
	h.Set("X-Content-Type-Options", "nosniff")
	// inline rather than attachment, and this one is a *functional* header
	// rather than a protective one -- attachment makes the iframe download the
	// file instead of drawing it. Said out loud because it looks like the
	// weakening of setAttachmentHeaders and is not: nothing above depends on
	// it, and the sandbox holds whatever the disposition says.
	h.Set("Content-Disposition", `inline; filename="`+asciiFilename(name)+`"; filename*=UTF-8''`+rfc5987(name))
	// A project's bytes should not sit in a shared cache under the panel's
	// origin, and a preview is cheap to fetch again.
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// LimitReader as well as the Stat above: an agent may be writing this file
	// right now, so the size that was checked is not the size that arrives.
	_, _ = io.Copy(w, io.LimitReader(f, previewMaxBytes))
}
