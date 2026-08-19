BINARY := fitr
VERSION := 0.2.0-dev
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test vet fmt lint dist clean install spec-sync screenshots

all: fmt vet test build

## spec/ at the repo root is canonical; go:embed cannot reach outside a package
## directory, so a copy lives beside the code that embeds it. This target keeps
## the copies honest, and drift tests in internal/eval and internal/device fail
## the build when they diverge.
spec-sync:
	@rm -rf internal/eval/tasks internal/device/profiles
	@mkdir -p internal/eval/tasks internal/device/profiles
	@cp -r spec/tasks/. internal/eval/tasks/
	@cp spec/version.json internal/eval/tasks/version.json
	@cp -r spec/profiles/. internal/device/profiles/
	@echo "spec synced"

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fitr

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

## dist cross-compiles every supported target. No runtime, no interpreter,
## no package manager on the user's machine -- that is the whole argument.
dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -ldflags="$(LDFLAGS)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/fitr; \
	done
	@ls -lh dist/

install: build
	go install -ldflags="$(LDFLAGS)" ./cmd/fitr

clean:
	rm -rf dist $(BINARY) $(BINARY).exe

## Regenerate the README's terminal images from mock data through the real
## display code paths - screenshots that cannot drift from the renderer.
screenshots:
	@go run ./cmd/fitr screenshots docs/assets
