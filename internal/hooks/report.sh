#!/bin/sh
# vibepanel session state reporter.
#
# Installed once, globally, into your agent's hook configuration. It reports
# what the agent is doing so the panel does not have to guess from the byte
# stream.
#
# Safe to install globally: outside a vibepanel session the environment
# variables below are absent and this exits immediately, so agents you start
# from an ordinary terminal are unaffected.
#
# Usage: vibepanel-report.sh <working|waiting|done>

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

# --insecure is safe and necessary here: the destination is 127.0.0.1, and when
# the panel is serving TLS its certificate is issued for the public hostname,
# which a loopback address will never match.
curl --silent --show-error --insecure --max-time 2 --output /dev/null \
  --request POST "$VIBEPANEL_URL/api/hook/state" \
  --header "Authorization: Bearer $VIBEPANEL_TOKEN" \
  --header 'Content-Type: application/json' \
  --data "{\"sessionId\":\"$VIBEPANEL_SESSION_ID\",\"state\":\"$state\"}" \
  >/dev/null 2>&1

exit 0
