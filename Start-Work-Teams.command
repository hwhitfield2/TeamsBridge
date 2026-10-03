#!/bin/sh
set -eu
cd "$(dirname "$0")"
umask 077
exec python3 scripts/while-beeper-open.py
