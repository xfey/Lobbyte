#!/bin/sh

set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
template_dir="$project_root/packaging/npm"
package_json="$template_dir/package.json"
cd "$project_root"

node -e 'if (Number(process.versions.node.split(".")[0]) < 22) process.exit(1)' || {
	echo "Node.js 22 or newer is required." >&2
	exit 1
}

metadata() {
	node -e '
		const value = require(process.argv[1]).lobbyteRelease[process.argv[2]];
		if (typeof value !== "string") process.exit(1);
		process.stdout.write(value);
	' "$package_json" "$1"
}

version=${LOBBYTE_VERSION:-$(node -p "require('$package_json').version")}
node -e '
	const version = process.argv[1];
	if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/.test(version)) process.exit(1);
' "$version" || {
	echo "LOBBYTE_VERSION must be a simple semantic version." >&2
	exit 1
}

relay_url=$(metadata relayURL)
rules_version=$(metadata rulesVersion)
rules_url=$(metadata rulesURL)
privacy_url=$(metadata privacyURL)
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
	echo "Candidate Relay and admission metadata are invalid." >&2
	exit 1
}

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/lobbyte-npm-build.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
stage_dir="$work_dir/package"
mkdir -p "$stage_dir/bin" "$stage_dir/vendor"

cp "$package_json" "$stage_dir/package.json"
cp "$project_root/README.md" "$stage_dir/README.md"
cp "$project_root/LICENSE" "$stage_dir/LICENSE"
"$project_root/scripts/generate-third-party-notices.sh" "$stage_dir/THIRD_PARTY_NOTICES"
cp "$template_dir/bin/lobbyte.js" "$stage_dir/bin/lobbyte.js"
chmod 755 "$stage_dir/bin/lobbyte.js"

if [ "$version" != "$(node -p "require('$package_json').version")" ]; then
	node -e '
		const fs = require("node:fs");
		const path = process.argv[1];
		const value = JSON.parse(fs.readFileSync(path, "utf8"));
		value.version = process.argv[2];
		fs.writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`);
	' "$stage_dir/package.json" "$version"
fi

ldflags="-X main.clientVersion=$version -X main.rulesVersion=$rules_version -X main.relayURL=$relay_url"
if [ -n "$rules_url" ]; then
	ldflags="$ldflags -X main.rulesURL=$rules_url"
fi
if [ -n "$privacy_url" ]; then
	ldflags="$ldflags -X main.privacyURL=$privacy_url"
fi

for architecture in arm64 amd64; do
	output_architecture=$architecture
	if [ "$architecture" = "amd64" ]; then
		output_architecture=x64
	fi
	CGO_ENABLED=0 GOOS=darwin GOARCH=$architecture \
		go build -trimpath -buildvcs=false -ldflags "$ldflags" \
		-o "$stage_dir/vendor/lobbyte-darwin-$output_architecture" "$project_root/cmd/lobbyte"
done

(
	cd "$stage_dir"
	shasum -a 256 vendor/lobbyte-darwin-arm64 vendor/lobbyte-darwin-x64 >checksums.txt
)

node --test "$template_dir/launcher.test.js" >&2
candidate_dir=${LOBBYTE_ARTIFACT_DIR:-"$project_root/artifacts/npm"}
case "$candidate_dir" in
/*) ;;
*) candidate_dir="$project_root/$candidate_dir" ;;
esac
mkdir -p "$candidate_dir"
tarball_name=$(cd "$stage_dir" && npm pack --silent --pack-destination "$work_dir")
tarball="$work_dir/$tarball_name"
"$project_root/scripts/verify-npm-candidate.sh" "$tarball" "$version" >&2

destination="$candidate_dir/$tarball_name"
cp "$tarball" "$destination"
(
	cd "$candidate_dir"
	shasum -a 256 "$tarball_name" >"$tarball_name.sha256"
)
printf '%s\n' "$destination"
