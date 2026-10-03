#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
if [ ! -f config.yaml ]; then
  echo 'Missing config.yaml. See README.md for Beeper setup.' >&2
  exit 1
fi
chmod 600 config.yaml
exec ./bin/teamsbridge -c config.yaml
