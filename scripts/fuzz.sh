#!/bin/sh

set -eu

duration="${LOBBYTE_FUZZ_TIME:-2s}"

go test ./internal/protocol -run '^$' -fuzz '^FuzzDecode$' -fuzztime "$duration"
go test ./internal/textcheck -run '^$' -fuzz '^FuzzValidateMessage$' -fuzztime "$duration"
