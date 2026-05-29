#!/bin/sh
set -e
if [ "$#" -eq 0 ]; then
	set -- ./...
fi
go test -tags=goolm "$@"
