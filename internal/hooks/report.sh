#!/bin/sh
# vibepanel session state reporter.
#
# Installed once, globally, into your agent's hook configuration. It reports
# what the agent is doing so the panel does not have to guess from the byte
# stream, and forwards what the agent said so the panel can pass it on.
#
# Safe to install globally: outside a vibepanel session the environment
# variables below are absent and this exits immediately, so agents you start
# from an ordinary terminal are unaffected.
#
# Usage: vibepanel-report.sh <working|waiting|done> [codex|codex-notify]
#
# The second argument says which agent's hook is calling, where that changes how
# the panel reads the report: `codex-notify` is Codex's older `notify` line,
# which can only ever say "waiting". Anything after it -- Codex appends a JSON
# argument to notify -- is ignored.
#
# The hook's own document -- Claude Code, Codex, Kimi Code and zcode all pipe a
# JSON object to stdin with the event's name, the transcript path and, on Stop,
# the agent's last message -- is sent as the request body, untouched. The state
# travels in the query string so that a document the panel cannot read costs
# the message and never the state.

# Never fail, never block, never print. A hook that makes an agent wait — or
# worse, error — is far more expensive than a missed state update.
[ -n "$VIBEPANEL_SESSION_ID" ] || exit 0
[ -n "$VIBEPANEL_TOKEN" ] || exit 0
[ -n "$VIBEPANEL_URL" ] || exit 0

state="$1"
case "$state" in
  working|waiting|done) ;;
  *) exit 0 ;;
esac

source=""
case "${2-}" in
  codex|codex-notify) source="$2" ;;
  # An install from before codex hooks: notify = [script, "waiting"], to which
  # Codex appends its JSON. That is the legacy line, and it says so by shape.
  \{*) source="codex-notify" ;;
esac

# Read the document only from a pipe. A terminal on stdin would mean waiting
# for somebody to press ^D, in a hook whose one rule is never to wait. 256 KiB
# is the panel's own cap; reading past it would only be discarded there.
body=""
if [ ! -t 0 ]; then
  body=$(head -c 262144 2>/dev/null)
fi

# --insecure is safe and necessary here: the destination is 127.0.0.1, and when
# the panel is serving TLS its certificate is issued for the public hostname,
# which a loopback address will never match.
curl --silent --show-error --insecure --max-time 2 --output /dev/null \
  --request POST "$VIBEPANEL_URL/api/hook/state?sessionId=$VIBEPANEL_SESSION_ID&state=$state&source=$source" \
  --header "Authorization: Bearer $VIBEPANEL_TOKEN" \
  --header 'Content-Type: application/json' \
  --data-binary "$body" \
  >/dev/null 2>&1

exit 0
