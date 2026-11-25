#!/bin/sh
# First start without Home Assistant: data/options.json and a random token. Never overwrites them.
set -eu
umask 077
cd "$(dirname "$0")"
mkdir -p data
TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')"
if [ -e data/options.json ]; then
    echo "data/options.json is already there: not changed | już istnieje: bez zmian"
else
    sed "s|\"studio_token\": \"\"|\"studio_token\": \"$TOKEN\"|" options.example.json > data/options.json
    echo "Created data/options.json | Utworzono data/options.json"
fi
