# M=03 picks a milestone by number.
#   make check M=01        run milestone 01's acceptance tests (verbose, never cached)
#   make race M=04         the same, under the race detector
#   make progress          one line per milestone
#   make compare M=02      diff your server/ and cmd/ against checkpoint 02
#   make checkpoint M=02   back up your code to .backup/, then replace it with checkpoint 02
#   make build             build bin/devmcp
#   make verify            (maintainers) checkpoints pass; the starter compiles and fails milestones 1–3
.PHONY: check race progress compare checkpoint build verify
CP = _checkpoints/$(M)

check:
	@test -n "$(M)" || { echo "usage: make check M=01"; exit 2; }
	go test -count=1 -v ./acceptance/m$(M)/ $(if $(RUN),-run '$(RUN)')

race:
	@test -n "$(M)" || { echo "usage: make race M=04"; exit 2; }
	go test -race -count=1 ./acceptance/m$(M)/

progress:
	@go run ./tools/progress

compare:
	@test -d "$(CP)" || { echo "usage: make compare M=02 (no $(CP))"; exit 2; }
	-diff -ru $(CP)/server server
	-diff -ru $(CP)/cmd cmd

checkpoint:
	@test -d "$(CP)" || { echo "usage: make checkpoint M=02 (no $(CP))"; exit 2; }
	@set -eu; mkdir -p .backup; b=$$(mktemp -d ".backup/$$(date +%Y%m%d-%H%M%S)-XXXXXX"); \
	 cp -R server "$$b/"; if [ -d cmd ]; then cp -R cmd "$$b/"; fi; \
	 rm -rf server cmd; cp -R $(CP)/. .; echo "your previous code is in $$b; now at checkpoint $(M)"

build:
	go build -o bin/devmcp ./cmd/devmcp

verify:
	scripts/verify-checkpoints.sh
