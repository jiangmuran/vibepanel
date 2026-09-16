package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The chat tables, and the shape of what they hold.
//
// Everything here is IM-agnostic on purpose. An adapter knows Telegram or
// 飞书 or 微信; this file knows a *channel* (a string naming the adapter), a
// *peer* (the opaque id the adapter gave the person), and a *ref* (the opaque
// id the adapter gave a message). Nothing in a column depends on which IM it
// came from, so a fourth adapter is a fourth string and not a migration.

// SessionMessage is one thing an agent said, as its hook reported it.
type SessionMessage struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId"`
	At        int64  `json:"at"`
	// Kind is why the agent spoke: MessageAssistant for a finished turn,
	// MessagePrompt for a permission prompt, MessageQuestion for a question
	// the agent is waiting on, MessageNotice for anything else the hook
	// carried. The server maps each agent's hook vocabulary onto these four;
	// the table stores the result and nothing agent-specific.
	Kind string `json:"kind"`
	Text string `json:"text"`
	// Tool is which agent said it, by the panel's own name for it (claude,
	// codex, opencode, kimi, zcode), or empty when the report did not say.
	Tool string `json:"tool"`
}

// The kinds a SessionMessage may carry. Strings rather than an enum type so
// this package stays below internal/chat; AddSessionMessage refuses anything
// not in the list, which is what stops a hook's own vocabulary landing here.
const (
	MessageAssistant = "assistant"
	MessagePrompt    = "prompt"
	MessageQuestion  = "question"
	MessageNotice    = "notice"
	// MessageUser is the line the person typed at the agent, from the
	// UserPromptSubmit hook: "context" on a phone shows both sides.
	MessageUser = "user"
)

// MessagesKeptPerSession bounds the rows one session may hold.
//
// The chat surface reads the last one, and "context" reads ten or so; a
// session that runs for a week says a few thousand things, and none of them
// past the first couple of hundred is something anyone will ask a phone for.
const MessagesKeptPerSession = 200

// ValidMessageKind reports whether k is one of the kinds above; the routing
// rules validate against it so a rule cannot name a kind no message has.
func ValidMessageKind(k string) bool { return validMessageKind(k) }

func validMessageKind(k string) bool {
	switch k {
	case MessageAssistant, MessagePrompt, MessageQuestion, MessageNotice, MessageUser:
		return true
	}
	return false
}

// AddSessionMessage appends one report and trims the session to
// MessagesKeptPerSession.
//
// The trim is here rather than in a sweep because the growth is per hook
// report, which is per agent turn: an hourly sweep would let a busy session
// hold an hour of turns, and the bound exists so the table cannot be made to
// grow by the thing that writes it.
func (d *DB) AddSessionMessage(ctx context.Context, m SessionMessage) (SessionMessage, error) {
	if m.SessionID == "" || !validMessageKind(m.Kind) {
		return SessionMessage{}, fmt.Errorf("store: not a session message worth keeping")
	}
	if m.At == 0 {
		m.At = now()
	}
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO session_messages (session_id, at, kind, text, tool) VALUES (?, ?, ?, ?, ?)`,
		m.SessionID, m.At, m.Kind, m.Text, m.Tool)
	if err != nil {
		return SessionMessage{}, fmt.Errorf("store: add session message: %w", err)
	}
	m.ID, _ = res.LastInsertId()
	_, err = d.sql.ExecContext(ctx,
		`DELETE FROM session_messages WHERE session_id = ? AND id NOT IN (
		     SELECT id FROM session_messages WHERE session_id = ? ORDER BY id DESC LIMIT ?)`,
		m.SessionID, m.SessionID, MessagesKeptPerSession)
	if err != nil {
		return m, fmt.Errorf("store: trim session messages: %w", err)
	}
	return m, nil
}

// ListSessionMessages returns the last n reports for a session, oldest first.
func (d *DB) ListSessionMessages(ctx context.Context, sessionID string, n int) ([]SessionMessage, error) {
	if n <= 0 || n > MessagesKeptPerSession {
		n = MessagesKeptPerSession
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, session_id, at, kind, text, tool FROM (
		     SELECT id, session_id, at, kind, text, tool FROM session_messages
		     WHERE session_id = ? ORDER BY id DESC LIMIT ?)
		 ORDER BY id ASC`, sessionID, n)
	if err != nil {
		return nil, fmt.Errorf("store: list session messages: %w", err)
	}
	defer rows.Close()
	var out []SessionMessage
	for rows.Next() {
		var m SessionMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.At, &m.Kind, &m.Text, &m.Tool); err != nil {
			return nil, fmt.Errorf("store: scan session message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LatestSessionMessage is the most recent report, and whether there is one.
func (d *DB) LatestSessionMessage(ctx context.Context, sessionID string) (SessionMessage, bool, error) {
	var m SessionMessage
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, session_id, at, kind, text, tool FROM session_messages
		 WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID).
		Scan(&m.ID, &m.SessionID, &m.At, &m.Kind, &m.Text, &m.Tool)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionMessage{}, false, nil
	}
	if err != nil {
		return SessionMessage{}, false, fmt.Errorf("store: latest session message: %w", err)
	}
	return m, true, nil
}

// SweepSessionMessages removes reports for sessions that no longer exist.
func (d *DB) SweepSessionMessages(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM session_messages WHERE session_id NOT IN (SELECT id FROM sessions)`)
	if err != nil {
		return fmt.Errorf("store: sweep session messages: %w", err)
	}
	return nil
}

// SessionTranscript is where an agent said it writes its own transcript.
type SessionTranscript struct {
	SessionID string
	Tool      string
	Path      string
	UpdatedAt int64
}

// SetSessionTranscript records the path a hook reported.
func (d *DB) SetSessionTranscript(ctx context.Context, sessionID, tool, path string) error {
	if sessionID == "" || path == "" {
		return fmt.Errorf("store: a transcript needs a session and a path")
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO session_transcripts (session_id, tool, path, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET tool = excluded.tool, path = excluded.path,
		     updated_at = excluded.updated_at`,
		sessionID, tool, path, now())
	if err != nil {
		return fmt.Errorf("store: set session transcript: %w", err)
	}
	return nil
}

// GetSessionTranscript returns the recorded path, if any.
func (d *DB) GetSessionTranscript(ctx context.Context, sessionID string) (SessionTranscript, bool, error) {
	var t SessionTranscript
	err := d.sql.QueryRowContext(ctx,
		`SELECT session_id, tool, path, updated_at FROM session_transcripts WHERE session_id = ?`,
		sessionID).Scan(&t.SessionID, &t.Tool, &t.Path, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionTranscript{}, false, nil
	}
	if err != nil {
		return SessionTranscript{}, false, fmt.Errorf("store: get session transcript: %w", err)
	}
	return t, true, nil
}

// ChatChannel is one configured adapter.
//
// ConfigEnc is the adapter's own configuration as sealed JSON. The store does
// not know what is in it, and that is the point: a bot token is a credential
// that lets anyone who has it speak as this panel, and the row must not be
// readable in a backup any more than a share link's token is.
type ChatChannel struct {
	Kind      string
	Enabled   bool
	ConfigEnc []byte
	UpdatedAt int64
}

// PutChatChannel creates or replaces a channel's row.
func (d *DB) PutChatChannel(ctx context.Context, c ChatChannel) error {
	if c.Kind == "" {
		return fmt.Errorf("store: a chat channel needs a kind")
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO chat_channels (kind, enabled, config_enc, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind) DO UPDATE SET enabled = excluded.enabled,
		     config_enc = excluded.config_enc, updated_at = excluded.updated_at`,
		c.Kind, c.Enabled, c.ConfigEnc, now())
	if err != nil {
		return fmt.Errorf("store: put chat channel: %w", err)
	}
	return nil
}

// GetChatChannel returns one channel, or ErrNotFound.
func (d *DB) GetChatChannel(ctx context.Context, kind string) (ChatChannel, error) {
	var c ChatChannel
	err := d.sql.QueryRowContext(ctx,
		`SELECT kind, enabled, config_enc, updated_at FROM chat_channels WHERE kind = ?`, kind).
		Scan(&c.Kind, &c.Enabled, &c.ConfigEnc, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatChannel{}, ErrNotFound
	}
	if err != nil {
		return ChatChannel{}, fmt.Errorf("store: get chat channel: %w", err)
	}
	return c, nil
}

// ListChatChannels returns every configured channel.
func (d *DB) ListChatChannels(ctx context.Context) ([]ChatChannel, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT kind, enabled, config_enc, updated_at FROM chat_channels ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("store: list chat channels: %w", err)
	}
	defer rows.Close()
	var out []ChatChannel
	for rows.Next() {
		var c ChatChannel
		if err := rows.Scan(&c.Kind, &c.Enabled, &c.ConfigEnc, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan chat channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChatChannel removes a channel and everything that was only meaningful
// with it: its paired and pending people, the messages it sent, its mutes.
func (d *DB) DeleteChatChannel(ctx context.Context, kind string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete chat channel: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, stmt := range []string{
		`DELETE FROM chat_channels WHERE kind = ?`,
		// Blocked people stay blocked: removing a channel to fix a token and
		// adding it back must not let back in the stranger who was shut out.
		`DELETE FROM chat_peers WHERE channel = ? AND status != 'blocked'`,
		`DELETE FROM chat_outbound WHERE channel = ?`,
		`DELETE FROM chat_status WHERE channel = ?`,
		`DELETE FROM chat_mutes WHERE channel = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, kind); err != nil {
			return fmt.Errorf("store: delete chat channel: %w", err)
		}
	}
	return tx.Commit()
}

// ChatPeer is a person on the far side of a channel.
//
// Status is the whole authorisation model: only PeerPaired is pushed to or
// listened to. A stranger who messages the bot becomes PeerPending with a
// pairing code, and stays there until the owner types that code into the
// panel. PeerBlocked is a pending peer the owner refused, kept so the same
// stranger does not become pending again every time they say hello.
type ChatPeer struct {
	Channel     string `json:"channel"`
	PeerID      string `json:"peerId"`
	Display     string `json:"display"`
	Status      string `json:"status"`
	PairingCode string `json:"pairingCode"`
	// Mode is how this person's messages are read: ModeNormal for commands
	// and addressed replies only, ModeAdvanced for natural language through
	// the assistant as well.
	Mode string `json:"mode"`
	// FocusSession is the session a bare reply goes to, subject to the
	// single-waiting rule in internal/chat.
	FocusSession string `json:"focusSession"`
	// ContextToken is whatever the adapter needs to speak to this person
	// unprompted. 微信's iLink hands one over with each inbound message and
	// requires it on every outbound one; other adapters leave it empty.
	ContextToken string `json:"-"`
	CreatedAt    int64  `json:"createdAt"`
	LastSeenAt   int64  `json:"lastSeenAt"`
}

// Peer statuses and modes.
const (
	PeerPending = "pending"
	PeerPaired  = "paired"
	PeerBlocked = "blocked"

	ModeNormal   = "normal"
	ModeAdvanced = "advanced"
)

func validPeerStatus(s string) bool {
	return s == PeerPending || s == PeerPaired || s == PeerBlocked
}

func validPeerMode(m string) bool { return m == ModeNormal || m == ModeAdvanced }

const peerColumns = `channel, peer_id, display, status, pairing_code, mode, focus_session,
	context_token, created_at, last_seen_at`

func scanPeer(sc scanner) (ChatPeer, error) {
	var p ChatPeer
	err := sc.Scan(&p.Channel, &p.PeerID, &p.Display, &p.Status, &p.PairingCode, &p.Mode,
		&p.FocusSession, &p.ContextToken, &p.CreatedAt, &p.LastSeenAt)
	return p, err
}

// PutChatPeer creates or replaces a peer.
func (d *DB) PutChatPeer(ctx context.Context, p ChatPeer) error {
	if p.Channel == "" || p.PeerID == "" || !validPeerStatus(p.Status) || !validPeerMode(p.Mode) {
		return fmt.Errorf("store: not a chat peer worth keeping")
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = now()
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO chat_peers (`+peerColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel, peer_id) DO UPDATE SET display = excluded.display,
		     status = excluded.status, pairing_code = excluded.pairing_code, mode = excluded.mode,
		     focus_session = excluded.focus_session, context_token = excluded.context_token,
		     last_seen_at = excluded.last_seen_at`,
		p.Channel, p.PeerID, p.Display, p.Status, p.PairingCode, p.Mode, p.FocusSession,
		p.ContextToken, p.CreatedAt, p.LastSeenAt)
	if err != nil {
		return fmt.Errorf("store: put chat peer: %w", err)
	}
	return nil
}

// TouchChatPeer records that a person spoke: when, what they are called and,
// where the IM needs one, the token that lets the panel answer. Narrow on
// purpose: the bridge holds a copy of the row read a moment ago, and writing
// the whole copy back would undo a pairing the settings page made meanwhile.
func (d *DB) TouchChatPeer(ctx context.Context, channel, peerID, display, contextToken string, at int64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE chat_peers SET last_seen_at = ?,
		     display = CASE WHEN ? = '' THEN display ELSE ? END,
		     context_token = CASE WHEN ? = '' THEN context_token ELSE ? END
		 WHERE channel = ? AND peer_id = ?`,
		at, display, display, contextToken, contextToken, channel, peerID)
	if err != nil {
		return fmt.Errorf("store: touch chat peer: %w", err)
	}
	return nil
}

// SetChatPeerFocus records which session a bare reply from this person goes
// to. Narrow for the reason TouchChatPeer is.
func (d *DB) SetChatPeerFocus(ctx context.Context, channel, peerID, sessionID string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE chat_peers SET focus_session = ? WHERE channel = ? AND peer_id = ?`, sessionID, channel, peerID)
	if err != nil {
		return fmt.Errorf("store: set chat peer focus: %w", err)
	}
	return nil
}

// PrunePendingPeers forgets strangers who never got paired and have not
// spoken since `before`, and reports how many pending rows remain: the
// bridge stops answering new strangers past a bound, so a flood of hellos
// cannot fill the table.
func (d *DB) PrunePendingPeers(ctx context.Context, before int64) (int, error) {
	if _, err := d.sql.ExecContext(ctx,
		`DELETE FROM chat_peers WHERE status = ? AND last_seen_at < ?`, PeerPending, before); err != nil {
		return 0, fmt.Errorf("store: prune pending peers: %w", err)
	}
	var n int
	if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_peers WHERE status = ?`, PeerPending).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count pending peers: %w", err)
	}
	return n, nil
}

// GetChatPeer returns one peer, or ErrNotFound.
func (d *DB) GetChatPeer(ctx context.Context, channel, peerID string) (ChatPeer, error) {
	p, err := scanPeer(d.sql.QueryRowContext(ctx,
		`SELECT `+peerColumns+` FROM chat_peers WHERE channel = ? AND peer_id = ?`, channel, peerID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChatPeer{}, ErrNotFound
	}
	if err != nil {
		return ChatPeer{}, fmt.Errorf("store: get chat peer: %w", err)
	}
	return p, nil
}

// ListChatPeers returns every peer, pending first so the settings page shows
// who is asking before who is in.
func (d *DB) ListChatPeers(ctx context.Context) ([]ChatPeer, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+peerColumns+` FROM chat_peers
		 ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'paired' THEN 1 ELSE 2 END, created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list chat peers: %w", err)
	}
	defer rows.Close()
	var out []ChatPeer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan chat peer: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PairedChatPeers returns the peers a push may go to.
func (d *DB) PairedChatPeers(ctx context.Context) ([]ChatPeer, error) {
	all, err := d.ListChatPeers(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, p := range all {
		if p.Status == PeerPaired {
			out = append(out, p)
		}
	}
	return out, nil
}

// DeleteChatPeer forgets a peer and what was sent to them.
func (d *DB) DeleteChatPeer(ctx context.Context, channel, peerID string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete chat peer: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, stmt := range []string{
		`DELETE FROM chat_peers WHERE channel = ? AND peer_id = ?`,
		`DELETE FROM chat_outbound WHERE channel = ? AND peer_id = ?`,
		`DELETE FROM chat_status WHERE channel = ? AND peer_id = ?`,
		`DELETE FROM chat_mutes WHERE channel = ? AND peer_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, channel, peerID); err != nil {
			return fmt.Errorf("store: delete chat peer: %w", err)
		}
	}
	return tx.Commit()
}

// ChatHandle returns the short number for a session, assigning the next one
// on first sight.
//
// Assigned lazily, so a panel with two hundred sessions in its history does not
// number them all the first time a phone asks for one, and numbers are given
// in the order sessions first became worth talking about.
func (d *DB) ChatHandle(ctx context.Context, sessionID string) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("store: a handle needs a session")
	}
	// One assignment at a time; see DB.handleMu. The settings page assigns
	// handles for every session it lists, so the second party is not
	// hypothetical.
	d.handleMu.Lock()
	defer d.handleMu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: chat handle: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var h int
	err = tx.QueryRowContext(ctx, `SELECT handle FROM chat_handles WHERE session_id = ?`, sessionID).Scan(&h)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: chat handle: %w", err)
	}
	// Never reused: MAX+1 rather than a free slot, so the number a person
	// remembers from this morning still means this morning's session.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(handle), 0) + 1 FROM chat_handles`).Scan(&h); err != nil {
		return 0, fmt.Errorf("store: chat handle: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_handles (session_id, handle) VALUES (?, ?)`, sessionID, h); err != nil {
		return 0, fmt.Errorf("store: chat handle: %w", err)
	}
	return h, tx.Commit()
}

// SessionByHandle resolves a number a person typed.
func (d *DB) SessionByHandle(ctx context.Context, handle int) (string, bool, error) {
	var id string
	err := d.sql.QueryRowContext(ctx, `SELECT session_id FROM chat_handles WHERE handle = ?`, handle).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: session by handle: %w", err)
	}
	return id, true, nil
}

// ChatHandles returns every assigned number, by session.
func (d *DB) ChatHandles(ctx context.Context) (map[string]int, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT session_id, handle FROM chat_handles`)
	if err != nil {
		return nil, fmt.Errorf("store: chat handles: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var h int
		if err := rows.Scan(&id, &h); err != nil {
			return nil, fmt.Errorf("store: scan chat handle: %w", err)
		}
		out[id] = h
	}
	return out, rows.Err()
}

// ChatOutbound is one message the panel sent, remembered so a reply that
// quotes it can be routed.
type ChatOutbound struct {
	Channel   string
	PeerID    string
	Ref       string
	SessionID string
	// Kind is what the message was: "status" for a session's state line,
	// "reply" for the answer to a command, "screen" for a capture. The
	// assistant's "the one before last" resolves over status messages only,
	// because those are the ones a person means.
	Kind string
	// MessageID is the session message this showed the person: the prompt a
	// card asked about, the question a list line quoted. Zero for anything
	// that showed no particular message.
	//
	// It is what makes "allow" mean *that* request. A session asks, is
	// answered, and asks again; a card, a quote or a bare "y" that is about
	// the first request must not approve the second, and the only thing that
	// tells them apart is which message the person was looking at.
	MessageID int64
	At        int64
}

// Outbound kinds.
const (
	OutboundStatus = "status"
	OutboundReply  = "reply"
	OutboundScreen = "screen"
)

// RecordChatOutbound remembers a sent message.
func (d *DB) RecordChatOutbound(ctx context.Context, o ChatOutbound) error {
	if o.Channel == "" || o.PeerID == "" || o.Ref == "" {
		return fmt.Errorf("store: an outbound record needs a channel, a peer and a ref")
	}
	if o.At == 0 {
		o.At = now()
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO chat_outbound (channel, peer_id, ref, session_id, kind, message_id, at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel, peer_id, ref) DO UPDATE SET session_id = excluded.session_id,
		     kind = excluded.kind, message_id = excluded.message_id, at = excluded.at`,
		o.Channel, o.PeerID, o.Ref, o.SessionID, o.Kind, o.MessageID, o.At)
	if err != nil {
		return fmt.Errorf("store: record chat outbound: %w", err)
	}
	return nil
}

// ChatOutboundByRef finds what a quoted message was about.
func (d *DB) ChatOutboundByRef(ctx context.Context, channel, peerID, ref string) (ChatOutbound, bool, error) {
	var o ChatOutbound
	err := d.sql.QueryRowContext(ctx,
		`SELECT channel, peer_id, ref, session_id, kind, message_id, at FROM chat_outbound
		 WHERE channel = ? AND peer_id = ? AND ref = ?`, channel, peerID, ref).
		Scan(&o.Channel, &o.PeerID, &o.Ref, &o.SessionID, &o.Kind, &o.MessageID, &o.At)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatOutbound{}, false, nil
	}
	if err != nil {
		return ChatOutbound{}, false, fmt.Errorf("store: chat outbound by ref: %w", err)
	}
	return o, true, nil
}

// RecentChatOutbound returns the last n messages sent to a peer, newest first,
// which is what "the one before last" is resolved against.
func (d *DB) RecentChatOutbound(ctx context.Context, channel, peerID string, n int) ([]ChatOutbound, error) {
	if n <= 0 || n > 100 {
		n = 20
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT channel, peer_id, ref, session_id, kind, message_id, at FROM chat_outbound
		 WHERE channel = ? AND peer_id = ? ORDER BY at DESC, rowid DESC LIMIT ?`, channel, peerID, n)
	if err != nil {
		return nil, fmt.Errorf("store: recent chat outbound: %w", err)
	}
	defer rows.Close()
	var out []ChatOutbound
	for rows.Next() {
		var o ChatOutbound
		if err := rows.Scan(&o.Channel, &o.PeerID, &o.Ref, &o.SessionID, &o.Kind, &o.MessageID, &o.At); err != nil {
			return nil, fmt.Errorf("store: scan chat outbound: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ChatShown reports whether a session message was ever shown to this person:
// a card, a list line or a reply that carried it. A bare "y" may only answer
// a request the person has seen.
func (d *DB) ChatShown(ctx context.Context, channel, peerID string, messageID int64) (bool, error) {
	if messageID <= 0 {
		return false, nil
	}
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_outbound WHERE channel = ? AND peer_id = ? AND message_id = ?`,
		channel, peerID, messageID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: chat shown: %w", err)
	}
	return n > 0, nil
}

// ChatOutboundsForMessage returns every status message, to anybody, that
// showed one session message: the copies of a card to retire once it is
// answered.
func (d *DB) ChatOutboundsForMessage(ctx context.Context, sessionID string, messageID int64) ([]ChatOutbound, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT channel, peer_id, ref, session_id, kind, message_id, at FROM chat_outbound
		 WHERE session_id = ? AND message_id = ? AND kind = ?`, sessionID, messageID, OutboundStatus)
	if err != nil {
		return nil, fmt.Errorf("store: chat outbounds for message: %w", err)
	}
	defer rows.Close()
	var out []ChatOutbound
	for rows.Next() {
		var o ChatOutbound
		if err := rows.Scan(&o.Channel, &o.PeerID, &o.Ref, &o.SessionID, &o.Kind, &o.MessageID, &o.At); err != nil {
			return nil, fmt.Errorf("store: scan chat outbound: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SweepChatOutbound drops sent-message records older than the cutoff.
//
// A quote of a message from last month is not a thing anybody does, and the
// table is one row per push to every peer forever otherwise.
func (d *DB) SweepChatOutbound(ctx context.Context, before int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM chat_outbound WHERE at < ?`, before)
	if err != nil {
		return fmt.Errorf("store: sweep chat outbound: %w", err)
	}
	return nil
}

// ChatStatusRef is the message an adapter keeps editing for a session, if one
// has been sent to this peer.
func (d *DB) ChatStatusRef(ctx context.Context, channel, peerID, sessionID string) (string, bool, error) {
	var ref string
	err := d.sql.QueryRowContext(ctx,
		`SELECT ref FROM chat_status WHERE channel = ? AND peer_id = ? AND session_id = ?`,
		channel, peerID, sessionID).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: chat status ref: %w", err)
	}
	return ref, true, nil
}

// SetChatStatusRef records which message is the session's status line for
// this peer.
func (d *DB) SetChatStatusRef(ctx context.Context, channel, peerID, sessionID, ref string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO chat_status (channel, peer_id, session_id, ref, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(channel, peer_id, session_id) DO UPDATE SET ref = excluded.ref,
		     updated_at = excluded.updated_at`,
		channel, peerID, sessionID, ref, now())
	if err != nil {
		return fmt.Errorf("store: set chat status ref: %w", err)
	}
	return nil
}

// ClearChatStatus forgets a session's status line for this peer, so the next
// change sends a fresh message rather than editing one that has scrolled away.
func (d *DB) ClearChatStatus(ctx context.Context, channel, peerID, sessionID string) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM chat_status WHERE channel = ? AND peer_id = ? AND session_id = ?`,
		channel, peerID, sessionID)
	if err != nil {
		return fmt.Errorf("store: clear chat status: %w", err)
	}
	return nil
}

// MuteChat silences one session for one peer until the given time; zero
// unmutes.
func (d *DB) MuteChat(ctx context.Context, channel, peerID, sessionID string, until int64) error {
	if until <= 0 {
		_, err := d.sql.ExecContext(ctx,
			`DELETE FROM chat_mutes WHERE channel = ? AND peer_id = ? AND session_id = ?`,
			channel, peerID, sessionID)
		if err != nil {
			return fmt.Errorf("store: unmute chat: %w", err)
		}
		return nil
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO chat_mutes (channel, peer_id, session_id, until) VALUES (?, ?, ?, ?)
		 ON CONFLICT(channel, peer_id, session_id) DO UPDATE SET until = excluded.until`,
		channel, peerID, sessionID, until)
	if err != nil {
		return fmt.Errorf("store: mute chat: %w", err)
	}
	return nil
}

// ChatMutedUntil reports when a mute ends, or zero when there is none in
// force at `at`.
func (d *DB) ChatMutedUntil(ctx context.Context, channel, peerID, sessionID string, at int64) (int64, error) {
	var until int64
	err := d.sql.QueryRowContext(ctx,
		`SELECT until FROM chat_mutes WHERE channel = ? AND peer_id = ? AND session_id = ? AND until > ?`,
		channel, peerID, sessionID, at).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: chat muted until: %w", err)
	}
	return until, nil
}

// AddChatSpend adds one assistant call's cost to the day's total and returns
// the new total.
//
// Day is the caller's date string, so the boundary is the panel's configured
// zone and not UTC's: a budget that resets at eight in the morning is a budget
// nobody understands.
func (d *DB) AddChatSpend(ctx context.Context, day string, usd float64) (float64, error) {
	if day == "" {
		return 0, fmt.Errorf("store: spend needs a day")
	}
	if usd < 0 {
		usd = 0
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: add chat spend: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chat_spend (day, usd, calls) VALUES (?, ?, 1)
		 ON CONFLICT(day) DO UPDATE SET usd = usd + excluded.usd, calls = calls + 1`, day, usd); err != nil {
		return 0, fmt.Errorf("store: add chat spend: %w", err)
	}
	var total float64
	if err := tx.QueryRowContext(ctx, `SELECT usd FROM chat_spend WHERE day = ?`, day).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: add chat spend: %w", err)
	}
	return total, tx.Commit()
}

// ChatSpend returns a day's total and call count.
func (d *DB) ChatSpend(ctx context.Context, day string) (usd float64, calls int, err error) {
	err = d.sql.QueryRowContext(ctx, `SELECT usd, calls FROM chat_spend WHERE day = ?`, day).Scan(&usd, &calls)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("store: chat spend: %w", err)
	}
	return usd, calls, nil
}
