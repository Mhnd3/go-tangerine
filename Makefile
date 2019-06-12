# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: gdex android ios gdex-cross swarm evm all test clean
.PHONY: gdex-linux gdex-linux-386 gdex-linux-amd64 gdex-linux-mips64 gdex-linux-mips64le
.PHONY: gdex-linux-arm gdex-linux-arm-5 gdex-linux-arm-6 gdex-linux-arm-7 gdex-linux-arm64
.PHONY: gdex-darwin gdex-darwin-386 gdex-darwin-amd64
.PHONY: gdex-windows gdex-windows-386 gdex-windows-amd64

GOBIN = $(shell pwd)/build/bin
GO ?= latest

gdex: libbls
	build/env.sh go run build/ci.go install ./cmd/gdex
	@echo "Done building."
	@echo "Run \"$(GOBIN)/gdex\" to launch gdex."

swarm:
	build/env.sh go run build/ci.go install ./cmd/swarm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/swarm\" to launch swarm."

all:
	build/env.sh go run build/ci.go install

android:
	build/env.sh go run build/ci.go aar --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/gdex.aar\" to use the library."

ios:
	build/env.sh go run build/ci.go xcode --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/Geth.framework\" to use the library."

test: all libbls
	build/env.sh go run build/ci.go test

lint: ## Run linters.
	build/env.sh go run build/ci.go lint

libbls:
	make -C vendor/github.com/dexon-foundation/bls lib/libbls384.a

clean:
	./build/clean_go_build_cache.sh
	rm -fr build/_workspace/pkg/ $(GOBIN)/*
	make -C vendor/github.com/dexon-foundation/bls clean
	make -C vendor/github.com/dexon-foundation/mcl clean

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/kevinburke/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go get -u github.com/golang/protobuf/protoc-gen-go
	env GOBIN= go install ./cmd/abigen
	@type "npm" 2> /dev/null || echo 'Please install node.js and npm'
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

swarm-devtools:
	env GOBIN= go install ./cmd/swarm/mimegen

# Cross Compilation Targets (xgo)

gdex-cross: gdex-linux gdex-darwin gdex-windows gdex-android gdex-ios
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/gdex-*

gdex-linux: gdex-linux-386 gdex-linux-amd64 gdex-linux-arm gdex-linux-mips64 gdex-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-*

gdex-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./cmd/gdex
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep 386

gdex-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./cmd/gdex
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep amd64

gdex-linux-arm: gdex-linux-arm-5 gdex-linux-arm-6 gdex-linux-arm-7 gdex-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep arm

gdex-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./cmd/gdex
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep arm-5

gdex-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./cmd/gdex
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep arm-6

gdex-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./cmd/gdex
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep arm-7

gdex-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./cmd/gdex
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep arm64

gdex-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./cmd/gdex
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep mips

gdex-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./cmd/gdex
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep mipsle

gdex-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./cmd/gdex
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep mips64

gdex-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./cmd/gdex
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/gdex-linux-* | grep mips64le

gdex-darwin: gdex-darwin-386 gdex-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/gdex-darwin-*

gdex-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./cmd/gdex
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-darwin-* | grep 386

gdex-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./cmd/gdex
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-darwin-* | grep amd64

gdex-windows: gdex-windows-386 gdex-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/gdex-windows-*

gdex-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./cmd/gdex
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-windows-* | grep 386

gdex-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./cmd/gdex
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdex-windows-* | grep amd64
