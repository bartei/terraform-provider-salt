#!/usr/bin/env bash
#
# vm.sh — manage a QEMU Debian VM for acceptance testing
#
# Usage:
#   ./vm.sh start   — download image (if needed), generate SSH key, boot VM
#   ./vm.sh stop    — kill the VM and clean up
#   ./vm.sh status  — check if the VM is running
#   ./vm.sh ssh     — open an interactive SSH session to the VM
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VM_DIR="${SCRIPT_DIR}/.vm"

# VM configuration
DEBIAN_VERSION="12"
IMAGE_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-${DEBIAN_VERSION}-generic-amd64.qcow2"
BASE_IMAGE="${VM_DIR}/debian-${DEBIAN_VERSION}-base.qcow2"
VM_DISK="${VM_DIR}/vm-disk.qcow2"
CIDATA_ISO="${VM_DIR}/cidata.iso"
SSH_KEY="${VM_DIR}/id_ed25519"
PIDFILE="${VM_DIR}/qemu.pid"
SSH_PORT="${ACC_SSH_PORT:-2222}"
VM_MEMORY="${ACC_VM_MEMORY:-1024}"

log() { echo "==> $*" >&2; }

ensure_vm_dir() {
    mkdir -p "${VM_DIR}"
}

download_image() {
    if [[ -f "${BASE_IMAGE}" ]]; then
        log "Base image already exists: ${BASE_IMAGE}"
        return
    fi
    log "Downloading Debian ${DEBIAN_VERSION} cloud image..."
    curl -fSL --progress-bar -o "${BASE_IMAGE}.tmp" "${IMAGE_URL}"
    mv "${BASE_IMAGE}.tmp" "${BASE_IMAGE}"
    log "Download complete."
}

generate_ssh_key() {
    if [[ -f "${SSH_KEY}" ]]; then
        log "SSH key already exists: ${SSH_KEY}"
        return
    fi
    log "Generating ephemeral SSH keypair..."
    ssh-keygen -t ed25519 -f "${SSH_KEY}" -N "" -q
}

create_cloud_init() {
    local pubkey
    pubkey="$(cat "${SSH_KEY}.pub")"

    log "Creating cloud-init seed ISO..."

    cat > "${VM_DIR}/meta-data" <<EOF
instance-id: tf-salt-acc-test
local-hostname: salt-test
EOF

    cat > "${VM_DIR}/user-data" <<EOF
#cloud-config
users:
  - name: test
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ${pubkey}

# Disable automatic apt updates to avoid lock contention during tests
package_update: false
package_upgrade: false

# Signal that cloud-init is done
runcmd:
  - touch /var/lib/cloud/instance/boot-finished-signal
EOF

    # Create the ISO — try genisoimage first, then mkisofs, then xorrisofs
    local iso_cmd=""
    if command -v genisoimage &>/dev/null; then
        iso_cmd="genisoimage"
    elif command -v mkisofs &>/dev/null; then
        iso_cmd="mkisofs"
    elif command -v xorrisofs &>/dev/null; then
        iso_cmd="xorrisofs"
    else
        log "ERROR: No ISO creation tool found. Install one of: genisoimage, mkisofs, xorrisofs"
        exit 1
    fi

    "${iso_cmd}" -output "${CIDATA_ISO}" -volid cidata -joliet -rock \
        "${VM_DIR}/user-data" "${VM_DIR}/meta-data" 2>/dev/null
}

create_vm_disk() {
    log "Creating VM disk (copy-on-write overlay)..."
    qemu-img create -f qcow2 -b "${BASE_IMAGE}" -F qcow2 "${VM_DISK}" 10G
}

start_vm() {
    if is_running; then
        log "VM is already running (PID $(cat "${PIDFILE}"))"
        return
    fi

    ensure_vm_dir
    download_image
    generate_ssh_key
    create_cloud_init
    create_vm_disk

    log "Starting QEMU VM (SSH port ${SSH_PORT}, memory ${VM_MEMORY}M)..."

    # Check for KVM support
    local accel_opts="-accel tcg"
    if [[ -w /dev/kvm ]]; then
        accel_opts="-accel kvm"
        log "Using KVM acceleration"
    else
        log "KVM not available, using TCG (slower)"
    fi

    qemu-system-x86_64 \
        ${accel_opts} \
        -m "${VM_MEMORY}" \
        -smp 2 \
        -display none \
        -drive file="${VM_DISK}",if=virtio,format=qcow2 \
        -drive file="${CIDATA_ISO}",if=virtio,media=cdrom \
        -netdev user,id=net0,hostfwd=tcp::${SSH_PORT}-:22 \
        -device virtio-net-pci,netdev=net0 \
        -pidfile "${PIDFILE}" \
        -daemonize \
        -serial file:"${VM_DIR}/console.log"

    log "VM started. Waiting for SSH..."
    wait_for_ssh
    log "VM is ready! SSH: ssh -p ${SSH_PORT} -i ${SSH_KEY} test@localhost"
}

wait_for_ssh() {
    local max_attempts=90
    local attempt=0
    while (( attempt < max_attempts )); do
        if ssh -p "${SSH_PORT}" -i "${SSH_KEY}" \
            -o StrictHostKeyChecking=no \
            -o UserKnownHostsFile=/dev/null \
            -o ConnectTimeout=2 \
            -o BatchMode=yes \
            test@localhost true 2>/dev/null; then
            return
        fi
        (( attempt++ )) || true
        sleep 2
    done
    log "ERROR: SSH did not become available after $((max_attempts * 2)) seconds"
    log "Console log:"
    tail -20 "${VM_DIR}/console.log" >&2
    exit 1
}

stop_vm() {
    if [[ -f "${PIDFILE}" ]]; then
        local pid
        pid="$(cat "${PIDFILE}")"
        if kill -0 "${pid}" 2>/dev/null; then
            log "Stopping VM (PID ${pid})..."
            kill "${pid}" 2>/dev/null || true
            # Wait for process to exit
            local i=0
            while kill -0 "${pid}" 2>/dev/null && (( i < 10 )); do
                sleep 1
                (( i++ )) || true
            done
            if kill -0 "${pid}" 2>/dev/null; then
                kill -9 "${pid}" 2>/dev/null || true
            fi
        else
            log "VM process ${pid} already exited"
        fi
    else
        log "VM is not running (no pidfile)"
    fi

    # Clean up ephemeral files (keep the base image for speed)
    rm -f "${VM_DISK}" "${CIDATA_ISO}" "${PIDFILE}" 2>/dev/null || true
    rm -f "${VM_DIR}/meta-data" "${VM_DIR}/user-data" "${VM_DIR}/console.log" 2>/dev/null || true
    rm -f "${SSH_KEY}" "${SSH_KEY}.pub" 2>/dev/null || true
    log "Cleaned up."
}

is_running() {
    [[ -f "${PIDFILE}" ]] && kill -0 "$(cat "${PIDFILE}")" 2>/dev/null
}

status_vm() {
    if is_running; then
        echo "running (PID $(cat "${PIDFILE}"))"
    else
        echo "stopped"
    fi
}

do_ssh() {
    exec ssh -p "${SSH_PORT}" -i "${SSH_KEY}" \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        test@localhost
}

case "${1:-help}" in
    start)  start_vm ;;
    stop)   stop_vm ;;
    status) status_vm ;;
    ssh)    do_ssh ;;
    *)
        echo "Usage: $0 {start|stop|status|ssh}" >&2
        exit 1
        ;;
esac
