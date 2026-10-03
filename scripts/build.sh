#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
export GOCACHE="$PWD/.cache/go-build"
go build -tags goolm -o bin/teamsbridge ./cmd/teamsbridge
