VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/arm64 windows/amd64

.PHONY: build test vet fmt-check cross bindir deb clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o crossdock ./cmd/crossdock

vet:
	go vet ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

test: vet
	go test -race ./...

# dist/crossdock-<os>-<arch>[.exe]
cross:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building crossdock $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/crossdock-$$os-$$arch$$ext ./cmd/crossdock || exit 1; \
	done

# dist/linux-<arch>/crossdock: the layout the package scripts take.
ARCH ?= amd64
bindir:
	@test -f dist/crossdock-linux-$(ARCH) || { echo "run make cross first"; exit 1; }
	@mkdir -p dist/linux-$(ARCH)
	cp dist/crossdock-linux-$(ARCH) dist/linux-$(ARCH)/crossdock

# A .deb for ARCH (amd64 or arm64) from the cross-built binary.
deb: bindir
	build-aux/linux/build-deb.sh "$(VERSION)" $(ARCH) dist/linux-$(ARCH) dist/crossdock-$(ARCH).deb

clean:
	rm -rf crossdock dist
