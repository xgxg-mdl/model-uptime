.DEFAULT_GOAL := check

GO ?= go
GOFMT ?= gofmt
NODE ?= node
NPM ?= npm
GOVULNCHECK_VERSION ?= v1.7.0
GO_PACKAGES ?= $(shell $(GO) list ./... | sed '/\/node_modules\//d')
GO_FILES := $(shell git ls-files --cached --others --exclude-standard -- '*.go' | while IFS= read -r file; do test ! -f "$$file" || printf '%s\n' "$$file"; done)

.PHONY: ci check deployment-test fmt fmt-check js-check mod-check race test vet vuln web-test

fmt:
	$(GOFMT) -w $(GO_FILES)

fmt-check:
	@unformatted="$$($(GOFMT) -l $(GO_FILES))" || exit $$?; \
	if [ -n "$$unformatted" ]; then \
		printf 'The following Go files need formatting:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

mod-check:
	$(GO) mod verify
	$(GO) mod tidy -diff

vet:
	$(GO) vet $(GO_PACKAGES)

check: fmt-check mod-check vet

test:
	$(GO) test $(GO_PACKAGES)

race:
	$(GO) test -race -count=1 $(GO_PACKAGES)

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(GO_PACKAGES)

web-test:
	$(NODE) --test tests/web/*.test.js

deployment-test:
	$(NODE) --test tests/deployment/*.test.js

js-check:
	$(NPM) run lint
	$(NPM) run format:check

ci: check race js-check web-test deployment-test vuln
