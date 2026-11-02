#!/usr/bin/env bash
# Maintainer check: every checkpoint NN must pass the acceptance tests of milestones 01..NN,
# and the starter must compile and fail every test in milestones 1–3 without panicking.
# Later milestones need Config fields that the starter deliberately does not define.
#   scripts/verify-checkpoints.sh          # all checkpoints
#   scripts/verify-checkpoints.sh 03       # just checkpoint 03
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
only="${1:-}"
fail=0
tmp=""
trap 'if [[ -n "$tmp" ]]; then rm -rf "$tmp"; fi' EXIT

if [[ $# -gt 1 || -n "$only" && ( ! "$only" =~ ^[0-9][0-9]$ || ! -d "$root/_checkpoints/$only" ) ]]; then
	echo "usage: $0 [checkpoint number, e.g. 03]" >&2
	exit 2
fi

for cp in "$root"/_checkpoints/[0-9][0-9]; do
	n="$(basename "$cp")"
	[[ -n "$only" && "$only" != "$n" ]] && continue
	tmp="$(mktemp -d)"
	rsync -a --exclude .git --exclude _checkpoints --exclude .backup --exclude bin "$root"/ "$tmp"/
	rm -rf "$tmp/server" "$tmp/cmd"
	cp -R "$cp"/. "$tmp"/
	pkgs=()
	for m in "$tmp"/acceptance/m[0-9][0-9]; do
		mn="$(basename "$m")"; mn="${mn#m}"
		[[ "$mn" > "$n" ]] || pkgs+=("./acceptance/m$mn/")
	done
	echo "== checkpoint $n: ${pkgs[*]}"
	if (cd "$tmp" && go vet ./internal/... ./server/... ./cmd/... "${pkgs[@]}" && go test -race -count=1 "${pkgs[@]}"); then :; else fail=1; fi
	rm -rf "$tmp"
	tmp=""
done

if [[ -z "$only" ]]; then
	echo "== starter code: milestones 1–3 must compile, then every test must fail without panicking"
	pkgs=(./acceptance/m01/ ./acceptance/m02/ ./acceptance/m03/)
	(cd "$root" && go test -run '^$' "${pkgs[@]}")
	status=0
	out="$(cd "$root" && go test -v -count=1 "${pkgs[@]}" 2>&1)" || status=$?
	passes="$(grep -c -- '--- PASS' <<<"$out" || true)"
	failures="$(grep -c -- '--- FAIL' <<<"$out" || true)"
	skips="$(grep -c -- '--- SKIP' <<<"$out" || true)"
	panics="$(grep -c 'panic:' <<<"$out" || true)"
	echo "passing tests: $passes, failing tests: $failures, skipped tests: $skips, panics: $panics"
	if [[ "$status" != 1 || "$failures" == 0 || "$passes" != 0 || "$skips" != 0 || "$panics" != 0 ]]; then
		printf '%s\n' "$out"
		fail=1
	fi
fi
exit $fail
