#!/bin/sh
# Development deploy: tests, then copy the app to a mounted Samba "addons" share
# (default /Volumes/addons, override with ADDONS=...). Home Assistant then rebuilds it as a local app.
set -e
HERE="$(cd "$(dirname "$0")" && pwd)"
ADDONS="${ADDONS:-/Volumes/addons}"
[ -d "$ADDONS" ] || { echo "Mount the Home Assistant addons share first ($ADDONS)"; exit 1; }
"$HERE/build.sh"
# Samba: --inplace, renaming temporary files is sometimes rejected
rsync -a --inplace --delete --exclude '.DS_Store' "$HERE/vector/" "$ADDONS/vector/"
echo "Deployed app $(grep '^version' "$HERE/vector/config.yaml")"
