#!/bin/sh
set -e
go build -tags=goolm -o ./ai "$@" ./cmd/ai
