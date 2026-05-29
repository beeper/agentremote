#!/bin/sh
set -e
go run -tags=goolm ./cmd/ai "$@"
