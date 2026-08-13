#!/bin/sh

set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: test-npm-candidate.sh <tarball>" >&2
	exit 2
fi

tarball=$1
project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
case "$tarball" in
/*) ;;
*) tarball=$(CDPATH= cd -- "$(dirname "$tarball")" && pwd -P)/$(basename "$tarball") ;;
esac
version=$(node -e '
	const fs = require("node:fs");
	const child = require("node:child_process");
	const tarball = process.argv[1];
	const json = child.execFileSync("tar", ["-xOf", tarball, "package/package.json"], {encoding: "utf8"});
	process.stdout.write(JSON.parse(json).version);
' "$tarball")
"$project_root/scripts/verify-npm-candidate.sh" "$tarball" "$version"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/lobbyte-npm-install.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
prefix="$work_dir/prefix"
test_home="$work_dir/home"
mkdir -p "$prefix" "$test_home/.codex" "$test_home/.claude"
printf '%s\n' '{"hooks":{}}' >"$test_home/.claude/settings.json"
printf '%s\n' '{"hooks":{}}' >"$test_home/.codex/hooks.json"

old_version=0.0.0-candidate
old_tarball=$(LOBBYTE_VERSION=$old_version LOBBYTE_ARTIFACT_DIR="$work_dir/old" \
	"$project_root/scripts/build-npm-candidate.sh")
npm install --global --prefix "$prefix" --ignore-scripts --no-audit --no-fund "$old_tarball" >/dev/null
launcher="$prefix/bin/lobbyte"
[ -x "$launcher" ]
[ "$(HOME="$test_home" XDG_CONFIG_HOME="$work_dir/config" XDG_CACHE_HOME="$work_dir/cache" "$launcher" --version)" = "lobbyte $old_version" ]

npm install --global --prefix "$prefix" --ignore-scripts --no-audit --no-fund "$tarball" >/dev/null
[ "$(HOME="$test_home" XDG_CONFIG_HOME="$work_dir/config" XDG_CACHE_HOME="$work_dir/cache" "$launcher" --version)" = "lobbyte $version" ]
HOME="$test_home" XDG_CONFIG_HOME="$work_dir/config" XDG_CACHE_HOME="$work_dir/cache" "$launcher" hooks remove

npm uninstall --global --prefix "$prefix" --ignore-scripts --no-audit --no-fund lobbyte >/dev/null
[ ! -e "$launcher" ]
[ ! -e "$prefix/lib/node_modules/lobbyte" ]
