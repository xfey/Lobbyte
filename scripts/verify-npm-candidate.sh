#!/bin/sh

set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: verify-npm-candidate.sh <tarball> <version>" >&2
	exit 2
fi

tarball=$1
expected_version=$2
project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
case "$tarball" in
/*) ;;
*) tarball=$(CDPATH= cd -- "$(dirname "$tarball")" && pwd -P)/$(basename "$tarball") ;;
esac
[ -f "$tarball" ] || {
	echo "Candidate tarball does not exist: $tarball" >&2
	exit 1
}

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/lobbyte-npm-verify.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
tar -xzf "$tarball" -C "$work_dir"
package_dir="$work_dir/package"

actual_files=$(cd "$package_dir" && find . -type f | LC_ALL=C sort)
expected_files='./LICENSE
./README.md
./THIRD_PARTY_NOTICES
./bin/lobbyte.js
./checksums.txt
./package.json
./vendor/lobbyte-darwin-arm64
./vendor/lobbyte-darwin-x64'
if [ "$actual_files" != "$expected_files" ]; then
	echo "Candidate package file allowlist mismatch:" >&2
	printf '%s\n' "$actual_files" >&2
	exit 1
fi
if find "$package_dir" -type l | grep -q .; then
	echo "Candidate package contains a symbolic link." >&2
	exit 1
fi

node -e '
	const value = require(process.argv[1]);
	const expected = process.argv[2];
	if (value.name !== "lobbyte" || value.version !== expected || value.author !== "xfey") process.exit(1);
	if (value.license !== "Apache-2.0" || value.bin?.lobbyte !== "bin/lobbyte.js") process.exit(1);
	if (value.scripts || value.dependencies || value.optionalDependencies || value.devDependencies) process.exit(1);
' "$package_dir/package.json" "$expected_version" || {
	echo "Candidate package metadata is invalid." >&2
	exit 1
}

relay_url=$(node -p "require('$package_dir/package.json').lobbyteRelease.relayURL")
rules_version=$(node -p "require('$package_dir/package.json').lobbyteRelease.rulesVersion")
rules_url=$(node -p "require('$package_dir/package.json').lobbyteRelease.rulesURL")
privacy_url=$(node -p "require('$package_dir/package.json').lobbyteRelease.privacyURL")
node -e '
	const relay = process.argv[1];
	const rulesVersion = process.argv[2];
	const rulesURL = process.argv[3];
	const privacyURL = process.argv[4];
	if (relay !== "wss://relay.lobbyte.online/v1/ws") process.exit(1);
	if (!/^[0-9A-Za-z._-]{1,64}$/.test(rulesVersion)) process.exit(1);
	if ((rulesURL === "") !== (privacyURL === "")) process.exit(1);
	for (const value of [rulesURL, privacyURL]) {
		if (!value) continue;
		let parsed;
		try { parsed = new URL(value); } catch { process.exit(1); }
		if (parsed.protocol !== "https:" || !parsed.hostname || /\s/.test(value)) process.exit(1);
	}
' "$relay_url" "$rules_version" "$rules_url" "$privacy_url" || {
	echo "Candidate release metadata is unsafe." >&2
	exit 1
}

[ -x "$package_dir/bin/lobbyte.js" ]
[ -x "$package_dir/vendor/lobbyte-darwin-arm64" ]
[ -x "$package_dir/vendor/lobbyte-darwin-x64" ]
(
	cd "$package_dir"
	shasum -a 256 -c checksums.txt
)

file "$package_dir/vendor/lobbyte-darwin-arm64" | grep -Eq 'Mach-O 64-bit executable arm64'
file "$package_dir/vendor/lobbyte-darwin-x64" | grep -Eq 'Mach-O 64-bit executable x86_64'
for binary in "$package_dir/vendor/lobbyte-darwin-arm64" "$package_dir/vendor/lobbyte-darwin-x64"; do
	otool -l "$binary" | awk '$1 == "minos" { found = 1; if ($2 != "12.0") exit 1 } END { if (!found) exit 1 }'
done

workspace=$(cd "$project_root" && pwd -P)
user_home=$(cd "${HOME:?HOME is required for the package privacy check}" && pwd -P)
for binary in "$package_dir/vendor/lobbyte-darwin-arm64" "$package_dir/vendor/lobbyte-darwin-x64"; do
	strings "$binary" | grep -Fxq "$expected_version"
	strings "$binary" | grep -Fxq "$rules_version"
	strings "$binary" | grep -Fxq "$relay_url"
	for forbidden in "$workspace" "$user_home" '.tmp/client-a' '.tmp/client-b' \
		'PRIVATE_MESSAGE' 'PRIVATE_SECRET'; do
		if strings "$binary" | grep -Fq "$forbidden"; then
			echo "Candidate client contains forbidden data ($forbidden): $binary" >&2
			exit 1
		fi
	done
done

dry_run=$(cd "$package_dir" && npm pack --dry-run --json 2>/dev/null)
DRY_RUN_JSON="$dry_run" node -e '
	const result = JSON.parse(process.env.DRY_RUN_JSON)[0];
	const actual = result.files.map((file) => file.path).sort();
	const expected = ["LICENSE", "README.md", "THIRD_PARTY_NOTICES", "bin/lobbyte.js", "checksums.txt", "package.json", "vendor/lobbyte-darwin-arm64", "vendor/lobbyte-darwin-x64"];
	if (JSON.stringify(actual) !== JSON.stringify(expected)) process.exit(1);
' || {
	echo "npm pack --dry-run did not match the package allowlist." >&2
	exit 1
}
