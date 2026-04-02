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

vm-start:
	@./test/acceptance/vm.sh start

vm-stop:
	@./test/acceptance/vm.sh stop

vm-status:
	@./test/acceptance/vm.sh status

vm-ssh:
	@./test/acceptance/vm.sh ssh

testacc-vm: vm-start
	TF_ACC=1 go test -v -count=1 -timeout 15m ./test/acceptance/...
	@echo "Tests complete. Run 'make vm-stop' to shut down the VM."

e2e: vm-start
	@./test/acceptance/e2e.sh
	@echo "E2E tests complete. Run 'make vm-stop' to shut down the VM."

.PHONY: build install lint generate test testacc vm-start vm-stop vm-status vm-ssh testacc-vm e2e
