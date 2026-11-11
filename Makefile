# Build on macOS or Linux; binaries target Linux because Firecracker is Linux-only.
GOARCH ?= arm64
ART    ?= $(HOME)/fc-artifacts
RUNS   ?= 20

.PHONY: all build agent test vet artifacts snapshot bench bench-cold clean

all: build agent

build:
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -o bin/fcbench-linux-$(GOARCH) ./cmd/fcbench

agent:
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/fc-agent-linux-$(GOARCH) ./cmd/fc-agent

test:
	go test ./...

vet:
	GOOS=linux GOARCH=$(GOARCH) go vet ./...

# ---- The targets below run inside the Linux VM ----

artifacts: agent
	bash ./scripts/fetch-artifacts.sh

snapshot:
	./bin/fcbench-linux-$(GOARCH) snapshot \
		-firecracker $(ART)/firecracker -kernel $(ART)/vmlinux -rootfs $(ART)/rootfs.ext4 \
		-out $(ART)/snap

bench:
	./bin/fcbench-linux-$(GOARCH) bench \
		-firecracker $(ART)/firecracker -snapshot $(ART)/snap -workdir $(ART)/runs \
		-runs $(RUNS) -json $(ART)/results-warm.json

bench-cold:
	sudo ./bin/fcbench-linux-$(GOARCH) bench \
		-firecracker $(ART)/firecracker -snapshot $(ART)/snap -workdir $(ART)/runs \
		-runs $(RUNS) -drop-caches -json $(ART)/results-cold.json

clean:
	rm -rf bin