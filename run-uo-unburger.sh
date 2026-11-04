#!/bin/sh
# uo-unburger — launcher for the Unicorn Overlord translation toolkit.
#
# Builds the toolkit into bin/uo-unburger when the binary is missing or older than
# the module files or the Go sources, then runs it. With no arguments the toolkit
# opens its keyboard-first interface when a terminal is attached, and prints its
# help otherwise; every argument is passed straight through, so this script can
# stand in for ./bin/uo-unburger anywhere.
#
# Usage:  ./run-uo-unburger.sh [command] [flags]
#
# The script only ever writes the binary inside this checkout (bin/uo-unburger,
# which is ignored by Git). It never contacts the network once the Go module cache
# is populated, and it replaces itself with the toolkit process, so exit codes and
# the terminal state are the toolkit's own.

set -u

# Resolve the checkout root from this script's own location, so it works from any
# working directory and from any shell (sh, bash, zsh, fish). Quoting and `pwd -P`
# keep paths containing spaces intact.
script_path=$0
case $script_path in
	*/*) script_dir=${script_path%/*} ;;
	*) script_dir=. ;;
esac
root=$(CDPATH= cd -- "$script_dir" && pwd -P) || exit 1

bin=$root/bin/uo-unburger
min_major=1
min_minor=26

# needs_build reports success when the binary is missing, not executable, or older
# than go.mod, go.sum or any Go source file.
needs_build() {
	[ -x "$bin" ] || return 0
	if [ -n "$(find "$root" -maxdepth 1 \( -name go.mod -o -name go.sum \) -newer "$bin" -print -quit 2>/dev/null)" ]; then
		return 0
	fi
	if [ -n "$(find "$root/cmd" "$root/internal" -type f -name '*.go' -newer "$bin" -print -quit 2>/dev/null)" ]; then
		return 0
	fi
	return 1
}

# build_tool checks the Go toolchain version and builds the toolkit. It is only
# called when a build is actually needed, so a machine without Go can still run an
# already-built binary.
build_tool() {
	if ! command -v go >/dev/null 2>&1; then
		echo "uo-unburger: Go $min_major.$min_minor or later is required to build the toolkit." >&2
		echo "             Install Go, or build it once with: go build -o bin/uo-unburger ./cmd/uo-unburger" >&2
		return 1
	fi
	go_version=$(go env GOVERSION 2>/dev/null || go version 2>/dev/null | awk '{print $3}')
	case $go_version in
		go*) ;;
		*) go_version=$(go version 2>/dev/null | awk '{print $3}') ;;
	esac
	numeric=${go_version#go}
	major=$(printf '%s' "$numeric" | sed 's/[^0-9].*//')
	rest=${numeric#*.}
	minor=$(printf '%s' "$rest" | sed 's/[^0-9].*//')
	if [ -z "$major" ] || [ -z "$minor" ]; then
		echo "uo-unburger: could not read the Go version (got '$go_version')." >&2
		return 1
	fi
	if [ "$major" -lt "$min_major" ] || { [ "$major" -eq "$min_major" ] && [ "$minor" -lt "$min_minor" ]; }; then
		echo "uo-unburger: Go $min_major.$min_minor or later is required (found $go_version)." >&2
		return 1
	fi
	mkdir -p "$root/bin" || return 1
	echo "uo-unburger: building $bin" >&2
	( cd "$root" && go build -trimpath -ldflags "-s -w" -o "$bin" ./cmd/uo-unburger ) || return 1
}

if needs_build; then
	build_tool || exit 1
fi

exec "$bin" "$@"
