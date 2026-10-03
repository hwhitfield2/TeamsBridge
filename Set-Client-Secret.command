#!/bin/sh
set -eu
cd "$(dirname "$0")"
exec python3 scripts/set-client-secret.py
