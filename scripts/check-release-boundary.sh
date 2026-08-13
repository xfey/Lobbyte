#!/bin/sh

set -eu

for forbidden in .agents docs ops; do
	if [ -e "$forbidden" ]; then
		echo "Internal-only path is present: $forbidden" >&2
		exit 1
	fi
done

if find cmd internal packaging scripts -name README.md -print | grep -q .; then
	echo "Subdirectory README indexes are not part of the public source tree." >&2
	exit 1
fi

private_markers=$(printf '%s|%s|%s|%s' \
	'Tencent-server-' '43\.128\.153\.' '/Users/' '@gmail\.com')
if rg -n "$private_markers" README.md CONTRIBUTING.md SECURITY.md Makefile cmd internal packaging \
	scripts --glob '!check-release-boundary.sh'; then
	echo "Private deployment or workstation metadata is present." >&2
	exit 1
fi

if git rev-parse --is-inside-work-tree >/dev/null 2>&1 && [ "$(git rev-list --count HEAD)" -gt 1 ]; then
	echo "The public repository must start from a clean one-commit history." >&2
	exit 1
fi
