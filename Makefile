BINARY  := fcvms
BIN_DIR := bin
N       ?= 2
export GOFLAGS := -mod=mod

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Download kernel, rootfs and install firecracker
	@./hack/fetch-resources.sh

.PHONY: build
build: ## Compile the launcher
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) .

.PHONY: run
run: build ## Boot N microVMs, default 2 (e.g. make run N=5); Ctrl+C cleans up
	@sudo -v
	@./$(BIN_DIR)/$(BINARY) -n $(N)

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format sources
	go fmt ./...

.PHONY: down
down: ## Remove leftover bridge, taps and firewall rules
	@sudo -v
	@./hack/teardown.sh

.PHONY: clean
clean: down ## Remove build output and per-VM disk images
	rm -rf $(BIN_DIR)
	rm -rf resources/vm*
	rm -f /tmp/firecracker*.sock*
