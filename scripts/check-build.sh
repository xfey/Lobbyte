#!/bin/sh

set -eu

output="$(mktemp -d "${TMPDIR:-/tmp}/lobbyte-build.XXXXXX")"
trap 'rm -rf "$output"' EXIT HUP INT TERM

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o "$output/lobbyte-darwin-arm64" ./cmd/lobbyte
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o "$output/lobbyte-darwin-amd64" ./cmd/lobbyte

workspace=$(pwd -P)
user_home=$(cd "${HOME:?HOME is required for the build privacy check}" && pwd -P)
for binary in "$output/lobbyte-darwin-arm64" "$output/lobbyte-darwin-amd64"; do
	for local_path in "$workspace" "$user_home" '.tmp/client-a' '.tmp/client-b'; do
		if strings "$binary" | grep -Fq "$local_path"; then
			echo "Build artifact contains a local path ($local_path): $binary" >&2
			exit 1
		fi
	done
done

for client in "$output/lobbyte-darwin-arm64" "$output/lobbyte-darwin-amd64"; do
	if strings "$client" | grep -Eq '(PRIVATE_MESSAGE|PRIVATE_SECRET)'; then
		echo "Client artifact unexpectedly contains test-secret data: $client" >&2
		exit 1
	fi
done
