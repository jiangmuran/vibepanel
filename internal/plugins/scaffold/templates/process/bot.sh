#!/bin/sh
# Runs as the panel's owner, supervised by the panel. The credential is in
# VIBEPANEL_PLUGIN_URL and reaches this plugin's API and nothing else;
# VIBEPANEL_PLUGIN_STATE is a directory of your own. Replace this with your
# program in whatever language: the shape is "poll the view, do something".
while :; do
  curl -fsS "${VIBEPANEL_PLUGIN_URL}view" > "$VIBEPANEL_PLUGIN_STATE/view.json" 2>/dev/null \
    && echo "$(date +%T) saw $(grep -o '"state"' "$VIBEPANEL_PLUGIN_STATE/view.json" | wc -l) sessions"
  sleep 30
done
