package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/vnc"
)

// The panel as a VNC proxy, in one paragraph, because the shape of it is the
// security argument and it is easy to undo by accident.
//
// The browser speaks RFB over a WebSocket; a VNC server speaks RFB over TCP.
// The bytes cross here, in handleVncSocket, and nowhere else. What the panel
// holds onto is one row per display -- a name, a host, a port, a view-only
// flag and a password -- and that row is the only place the address of an
// outbound connection ever comes from. The browser supplies an opaque id and
// nothing else. There is deliberately no endpoint anywhere that takes a host,
// a port or a URL and connects to it: a route that did would be SSRF with a
// nice interface, and it would look like a convenience while being written.
//
// Two layers, both server-side, and they answer different questions. The row
// answers "which display" and is written by somebody signed in. The policy in
// internal/vnc answers "may this process reach that address at all" and comes
// from a flag on the process, so nothing reachable from a browser can widen
// it. The row is checked against the policy when it is saved -- a target that
// could never work should be a refusal at the moment somebody types it, not a
// socket that fails forever afterwards -- and again on every connect, because
// a name resolves to whatever it resolves to today.

// maxVncCloseReason is the WebSocket close-reason limit. RFC 6455 gives the
// whole control frame 125 bytes and two of them are the code.
const maxVncCloseReason = 123

// vncReadLimit bounds one message from the browser.
//
// noVNC's messages are tens of bytes; a clipboard paste is the one that can be
// large. Without a limit the default is 32 KiB, which turns a long paste into
// a closed connection with no explanation.
const vncReadLimit = 4 << 20

func (s *Server) registerVncRoutes(r chi.Router) {
	r.Get("/vnc/targets", s.handleListVncTargets)
	r.Post("/vnc/targets", s.handleCreateVncTarget)
	r.Patch("/vnc/targets/{targetID}", s.handleUpdateVncTarget)
	r.Delete("/vnc/targets/{targetID}", s.handleDeleteVncTarget)
	r.Get("/vnc/targets/{targetID}/socket", s.handleVncSocket)
}

// vncPolicy is where the reachability rule comes from.
//
// A field on the Server whose zero value is the safe answer, for the reason
// TreeSampler is a value: every test that builds a Server by hand would
// otherwise have to remember it, and the one that forgot would be testing a
// panel with no policy at all. A zero Policy is loopback-only.
func (s *Server) vncPolicy() vnc.Policy { return s.VNC }

func (s *Server) handleListVncTargets(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListVncTargets(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(list))
}

type vncTargetRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	ViewOnly bool   `json:"viewOnly"`
	// Password is a pointer so that "leave it alone" and "clear it" are
	// different requests. A plain string cannot say the first, so every edit
	// of a display's name would have wiped its password -- silently, because
	// the field is never sent back for the client to notice was missing.
	Password *string `json:"password"`
}

func (s *Server) handleCreateVncTarget(w http.ResponseWriter, r *http.Request) {
	var in vncTargetRequest
	if !decode(w, r, &in) {
		return
	}
	existing, err := s.DB.ListVncTargets(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(existing) >= store.MaxVncTargets {
		writeErr(w, http.StatusRequestEntityTooLarge, "too many displays")
		return
	}
	target := store.VncTarget{ID: id.New()}
	if !s.applyVncTarget(w, r, &target, in) {
		return
	}
	made, err := s.DB.CreateVncTarget(r.Context(), target)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "vnc.added", u.Username, s.clientIP(r), vncDetail(made))
	}
	writeJSON(w, http.StatusOK, made)
}

func (s *Server) handleUpdateVncTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.DB.GetVncTarget(r.Context(), chi.URLParam(r, "targetID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var in vncTargetRequest
	if !decode(w, r, &in) {
		return
	}
	if !s.applyVncTarget(w, r, &target, in) {
		return
	}
	saved, err := s.DB.UpdateVncTarget(r.Context(), target)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "vnc.changed", u.Username, s.clientIP(r), vncDetail(saved))
	}
	writeJSON(w, http.StatusOK, saved)
}

// applyVncTarget merges a request onto a row and validates the result.
//
// One function for both create and update, because the validation IS the
// security boundary and two copies of it is one copy that will drift. The
// update path is the one that would drift: adding a field to create and
// forgetting update leaves an endpoint that can put an unchecked value into
// the row the socket handler dials.
func (s *Server) applyVncTarget(w http.ResponseWriter, r *http.Request, target *store.VncTarget,
	in vncTargetRequest) bool {
	host := strings.TrimSpace(in.Host)
	if host == "" {
		writeErr(w, http.StatusBadRequest, "a display needs a host")
		return false
	}
	if !vnc.ValidPort(in.Port) {
		writeErr(w, http.StatusBadRequest, "a display needs a port between 1 and 65535")
		return false
	}
	// Checked when it is written, not only when it is used.
	//
	// A target the policy will never reach is a display that shows an error
	// forever, and nothing on the settings page distinguishes that from a
	// machine that happens to be off. Refusing here says which of the two it
	// is, once, to the person who can fix it -- the same reasoning as
	// refusing a share link scoped to a project that does not exist.
	//
	// A name that does not resolve right now is NOT refused: a laptop that is
	// asleep is not a policy decision, and the connect path checks again.
	// So this rejects what is certainly wrong and lets through what is merely
	// unknown, which is the only split that does not lock somebody out of
	// their own configuration.
	if _, err := s.vncPolicy().Resolve(r.Context(), host); err != nil && errors.Is(err, vnc.ErrRefused) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	target.Name = strings.TrimSpace(in.Name)
	target.Host = host
	target.Port = in.Port
	target.ViewOnly = in.ViewOnly
	if in.Password != nil {
		target.Password = *in.Password
	}
	return true
}

func (s *Server) handleDeleteVncTarget(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "targetID")
	target, err := s.DB.GetVncTarget(r.Context(), targetID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if err := s.DB.DeleteVncTarget(r.Context(), targetID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "vnc.removed", u.Username, s.clientIP(r), vncDetail(target))
	}
	w.WriteHeader(http.StatusNoContent)
}

// vncDetail describes a display for an audit row.
//
// Only the CRUD is audited, never a connect. The browser reconnects on its own
// whenever a display is unreachable, so recording the attempt would write a
// row every few seconds for a machine that is switched off -- the same failure
// the allowlist rejections had, from the same cause. The decisions worth a
// permanent record are the human ones.
//
// Not net.JoinHostPort: this is read by a person, and the bracketing it adds
// around an IPv6 literal is noise there.
func vncDetail(t store.VncTarget) string {
	out := t.Host + ":" + strconv.Itoa(t.Port)
	if t.ViewOnly {
		out += " view-only"
	}
	if t.Password != "" {
		out += " password"
	}
	return out
}

// handleVncSocket is the one place RFB bytes cross between the browser and a
// VNC server.
func (s *Server) handleVncSocket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	target, err := s.DB.GetVncTarget(ctx, chi.URLParam(r, "targetID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}

	// OriginPatterns is deliberately absent, exactly as it is on /ws: left
	// nil, coder/websocket accepts a handshake only when Origin matches Host,
	// so a page on another site cannot open this socket with the browser's
	// cookies and be handed a live desktop.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.Log.Warn("vnc accept", "err", err)
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(vncReadLimit)

	upstream, err := s.vncPolicy().Dial(ctx, target.Host, target.Port)
	if err != nil {
		// A refusal and an unreachable machine are different answers and the
		// browser says different things about them: one is a configuration to
		// fix, the other is a machine to switch on.
		code := websocket.StatusBadGateway
		if errors.Is(err, vnc.ErrRefused) {
			code = websocket.StatusPolicyViolation
		}
		s.Log.Warn("vnc dial", "host", target.Host, "port", target.Port, "err", err)
		_ = c.Close(code, closeReason(err))
		return
	}
	defer upstream.Close()

	browser := websocket.NetConn(ctx, c, websocket.MessageBinary)
	if err := vnc.Handshake(upstream, browser, target.Password); err != nil {
		s.Log.Warn("vnc handshake", "host", target.Host, "port", target.Port, "err", err)
		_ = c.Close(websocket.StatusBadGateway, closeReason(err))
		return
	}
	if err := vnc.Proxy(upstream, browser, target.ViewOnly); err != nil {
		// Not a warning by default: the ordinary end of a VNC session is the
		// person closing the tab, which arrives here as a read error on one
		// side or the other.
		s.Log.Debug("vnc closed", "host", target.Host, "err", err)
		_ = c.Close(websocket.StatusBadGateway, closeReason(err))
		return
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// closeReason fits an error into a WebSocket close frame.
//
// Truncated by bytes and then made valid UTF-8 again: a close frame carrying a
// half-encoded rune is a protocol error, and the browser reports that instead
// of the message -- which would replace every explanation on this path with
// "connection closed abnormally".
func closeReason(err error) string {
	msg := err.Error()
	if len(msg) <= maxVncCloseReason {
		return msg
	}
	return strings.ToValidUTF8(msg[:maxVncCloseReason], "")
}
