#!/usr/bin/env bash
# Maintainer check: every checkpoint NN must pass the acceptance tests of milestones 01..NN,
# and the starter code must compile and fail every test without panicking.
#   scripts/verify-checkpoints.sh          # all checkpoints
#   scripts/verify-checkpoints.sh 03       # just checkpoint 03
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
only="${1:-}"
fail=0

for cp in "$root"/_checkpoints/[0-9][0-9]; do
	n="$(basename "$cp")"
	[[ -n "$only" && "$only" != "$n" ]] && continue
	tmp="$(mktemp -d)"
	rsync -a --exclude _checkpoints --exclude .backup --exclude bin "$root"/ "$tmp"/
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
done

if [[ -z "$only" ]]; then
	echo "== starter code: every test must fail, no panics"
	out="$(cd "$root" && go test -v -count=1 ./acceptance/... 2>&1 || true)"
	passes="$(grep -c -- '--- PASS' <<<"$out" || true)"
	panics="$(grep -c 'panic:' <<<"$out" || true)"
	echo "passing tests: $passes, panics: $panics"
	[[ "$passes" == 0 && "$panics" == 0 ]] || fail=1
fi
exit $fail
