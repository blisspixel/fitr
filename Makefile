BINARY := fitr
VERSION := 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test vet fmt lint dist clean install

all: fmt vet test build

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
