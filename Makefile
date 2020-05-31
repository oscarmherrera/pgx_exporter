PKGS := $(shell go list ./... | grep -v /vendor)

.PHONY: test
test: lint
	go test $(PKGS)

REPO_PATH=./cmd/pgx_exporter
BIN_DIR := $(GOPATH)/bin
GOMETALINTER := $(BIN_DIR)/gometalinter
LD_FLAGS="-w -X $(REPO_PATH)/cmd.Version=$(VERSION)"

$(GOMETALINTER):
	go get -u github.com/alecthomas/gometalinter
	gometalinter --install &> /dev/null

.PHONY: lint
lint: $(GOMETALINTER)
	gometalinter --disable=gotype ./... --vendor

BINARY := pgx_exporter
VERSION ?= 1.0.0
PLATFORMS := linux darwin
os = $(word 1, $@)

pgx_exporter:
	mkdir -p release
	CGO_ENABLED=0 GO111MODULE=on go build -ldflags $(LD_FLAGS) -o release/$(BINARY)-$(VERSION)-$(os)-amd64 $(REPO_PATH)/.
	cp release/$(BINARY)-$(VERSION)-$(os)-amd64 release/$(BINARY)

.PHONY: release
release: linux

check-makefile:
	cat -e -t -v Makefile