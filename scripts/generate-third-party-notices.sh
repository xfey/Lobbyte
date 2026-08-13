#!/bin/sh

set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: generate-third-party-notices.sh <output>" >&2
	exit 2
fi

output=$1
project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/lobbyte-license-notices.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
modules="$work_dir/modules"

cd "$project_root"
go list -deps -f '{{with .Module}}{{if and (not .Main) .Dir}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' \
	./cmd/lobbyte | awk 'NF' | LC_ALL=C sort -u >"$modules"

{
	printf '%s\n\n' 'Lobbyte includes the following third-party software:'
	while IFS='|' read -r module version directory; do
		license=$(find "$directory" -maxdepth 1 -type f \
			\( -iname 'LICENSE*' -o -iname 'COPYING*' \) -print | LC_ALL=C sort | head -n 1)
		if [ -z "$license" ]; then
			echo "No license file found for $module $version" >&2
			exit 1
		fi
		printf '%s\n' '-------------------------------------------------------------------------------'
		printf '%s %s\n' "$module" "$version"
		printf '%s\n\n' '-------------------------------------------------------------------------------'
		sed -e 's/[[:space:]]*$//' "$license"
		printf '\n\n'
	done <"$modules"
} >"$output"
