# Common tasks. Everything here also works as a plain command; the Makefile is
# a place to remember them, not a build system.

BIN := vibepanel

.PHONY: build
build: web            ## Build the binary with the frontend embedded
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/vibepanel

.PHONY: web
web:                  ## Build the frontend into internal/webui/dist
	cd web && npm run build

.PHONY: test
test:                 ## Go tests with the race detector, plus frontend units
	go test -race ./...
	cd web && npm run test

.PHONY: lint
lint:                 ## vet, gofmt and eslint
	go vet ./...
	@out="$$(gofmt -l . | grep -v '^web/' || true)"; \
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	cd web && npx tsc -b && npx eslint .

.PHONY: check
check: lint test      ## Fast gate: vet, gofmt, eslint, Go tests, frontend units

.PHONY: first-run-check
first-run-check: build ## The setup wizard and the first project, in a browser
	cd web && npm run check:first-run

.PHONY: render-check
render-check: build   ## Drive the real binary with a real browser
	cd web && npm run check:render

.PHONY: stress-check
stress-check: build   ## Wide characters, full-screen programs, floods, dropouts
	cd web && npm run check:stress

.PHONY: restart-check
restart-check: build  ## Kill the backend and check the sessions outlive it
	cd web && npm run check:restart

.PHONY: scale-check
scale-check: build    ## Two dozen sessions: snapshot size, sidebar, poller
	cd web && npm run check:scale

.PHONY: tls-check
tls-check: build      ## Serve over its own TLS: wss, Secure cookie, cert swap
	node scripts/tls-check.mjs ./$(BIN)

# Everything, in the order that fails fastest.
#
# `check` used to be described as "everything a change should pass before it
# lands", and it is not: it never starts a browser. The checks that drive the
# real binary have found the majority of the defects in this project — a note
# discarded by clicking the next tab, a phone rendering a desktop's grid at
# four pixels, a panel telling every link where it lived — and none of them are
# reachable from a unit test.
#
# Not merged into `check`, because this takes twenty minutes and a gate people
# stop running is worse than a slow one they run deliberately.
.PHONY: verify
verify: check first-run-check render-check stress-check restart-check scale-check tls-check release-check  ## Every check there is (~20 min)
	@echo "all checks passed"

.PHONY: release
release:              ## Cross-compiled archives in dist/
	scripts/build-release.sh

.PHONY: release-check
release-check:        ## Build the archives and run one from a throwaway HOME
	scripts/release-check.sh

.PHONY: clean
clean:
	rm -rf $(BIN) dist internal/webui/dist/assets internal/webui/dist/index.html

.PHONY: help
help:
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk -F':.*?## ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
