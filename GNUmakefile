default: build

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	go generate ./...

test:
	go test -v -count=1 -parallel=4 ./...

testacc:
	TF_ACC=1 go test -v -count=1 -parallel=4 -timeout 10m ./...

# --- Acceptance test VM (QEMU) ---
#
# Distro selection is controlled by DISTRO (default: debian). Supported:
# debian, fedora. To add a new distro, add a case in test/acceptance/vm.sh.
DISTRO ?= debian

vm-start:
	@DISTRO=$(DISTRO) ./test/acceptance/vm.sh start

vm-stop:
	@DISTRO=$(DISTRO) ./test/acceptance/vm.sh stop

vm-status:
	@DISTRO=$(DISTRO) ./test/acceptance/vm.sh status

vm-ssh:
	@DISTRO=$(DISTRO) ./test/acceptance/vm.sh ssh

testacc-vm: vm-start
	TF_ACC=1 go test -v -count=1 -timeout 15m ./test/acceptance/...
	@echo "Tests complete. Run 'make vm-stop' to shut down the VM."

e2e: vm-start
	@DISTRO=$(DISTRO) ./test/acceptance/e2e.sh
	@echo "E2E tests complete on $(DISTRO). Run 'make vm-stop DISTRO=$(DISTRO)' to shut down the VM."

# Convenience aliases for the distros currently in CI.
e2e-debian:
	@$(MAKE) e2e DISTRO=debian

e2e-fedora:
	@$(MAKE) e2e DISTRO=fedora

.PHONY: build install lint generate test testacc vm-start vm-stop vm-status vm-ssh testacc-vm e2e e2e-debian e2e-fedora
