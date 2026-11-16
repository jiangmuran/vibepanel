#!/bin/sh
set -eu
for f in "$@"; do
  echo "file: $f"  # quoted
done
