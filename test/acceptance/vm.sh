#!/usr/bin/env bash
#
# vm.sh — manage a QEMU VM for acceptance testing.
#
# Supports multiple distros via the DISTRO env var. Add a new distro by
# defining it in `configure_distro` below — no other changes needed.
#
# Usage:
#   DISTRO=debian ./vm.sh start    # default
#   DISTRO=fedora ./vm.sh start
#   ./vm.sh stop | status | ssh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VM_DIR="${SCRIPT_DIR}/.vm"

# Per-distro VM configuration. To add a new distro:
#   1. Add a new case branch with IMAGE_URL and IMAGE_FILENAME.
#   2. (Optional) override SELINUX_PERMISSIVE if the image ships SELinux
#      enforcing — we set it permissive during test to avoid surprises.
configure_distro() {
    DISTRO="${DISTRO:-debian}"
    SELINUX_PERMISSIVE="false"

    case "${DISTRO}" in
        debian)
            IMAGE_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2"
            IMAGE_FILENAME="debian-12-base.qcow2"
            ;;
        fedora)
            IMAGE_URL="https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2"
            IMAGE_FILENAME="fedora-42-base.qcow2"
            SELINUX_PERMISSIVE="true"
            ;;
        *)
            echo "ERROR: unknown DISTRO='${DISTRO}'. Supported: debian, fedora" >&2
            exit 1
            ;;
    esac

    BASE_IMAGE="${VM_DIR}/${IMAGE_FILENAME}"
    VM_DISK="${VM_DIR}/${DISTRO}-vm-disk.qcow2"
    CIDATA_ISO="${VM_DIR}/${DISTRO}-cidata.iso"
    SSH_KEY="${VM_DIR}/id_ed25519"
    PIDFILE="${VM_DIR}/${DISTRO}-qemu.pid"
}

configure_distro

SSH_PORT="${ACC_SSH_PORT:-2222}"
VM_MEMORY="${ACC_VM_MEMORY:-1024}"

log() { echo "==> [${DISTRO}] $*" >&2; }

ensure_vm_dir() {
    mkdir -p "${VM_DIR}"
}

download_image() {
    if [[ -f "${BASE_IMAGE}" ]]; then
        log "Base image already exists: ${BASE_IMAGE}"
        return
    fi
    log "Downloading ${DISTRO} cloud image..."
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
instance-id: tf-salt-acc-test-${DISTRO}
local-hostname: salt-test-${DISTRO}
EOF

    # Build runcmd entries dynamically so distros that need extra setup
    # (e.g. SELinux permissive on Fedora) can express it here without a
    # second cloud-init template.
    local runcmd_extras=""
    if [[ "${SELINUX_PERMISSIVE}" == "true" ]]; then
        runcmd_extras="
  - setenforce 0 || true
  - sed -i 's/^SELINUX=enforcing/SELINUX=permissive/' /etc/selinux/config || true"
    fi

    cat > "${VM_DIR}/user-data" <<EOF
#cloud-config
users:
  - name: test
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ${pubkey}

# Disable automatic package updates to avoid lock contention during tests
package_update: false
package_upgrade: false

runcmd:${runcmd_extras}
  - touch /var/lib/cloud/instance/boot-finished-signal
EOF

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
        -serial file:"${VM_DIR}/${DISTRO}-console.log"

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
    tail -20 "${VM_DIR}/${DISTRO}-console.log" >&2
    exit 1
}

stop_vm() {
    if [[ -f "${PIDFILE}" ]]; then
        local pid
        pid="$(cat "${PIDFILE}")"
        if kill -0 "${pid}" 2>/dev/null; then
            log "Stopping VM (PID ${pid})..."
            kill "${pid}" 2>/dev/null || true
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

    rm -f "${VM_DISK}" "${CIDATA_ISO}" "${PIDFILE}" 2>/dev/null || true
    rm -f "${VM_DIR}/meta-data" "${VM_DIR}/user-data" "${VM_DIR}/${DISTRO}-console.log" 2>/dev/null || true
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
        echo "Usage: DISTRO={debian|fedora} $0 {start|stop|status|ssh}" >&2
        exit 1
        ;;
esac