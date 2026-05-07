package salt

import "sync"

// hostLocks holds one *sync.Mutex per remote host. Callers serialize all
// on-host Salt operations (bootstrap, file uploads, salt-call apply/test)
// for a given target so that concurrent salt_state / salt_formula resources
// running in parallel under Terraform's default -parallelism=10 don't:
//
//   - race on bootstrap-salt.sh's /tmp FIFO and the dnf/rpm package lock,
//     which manifests as:
//     "ERROR: Failed to create the named pipe required to log"
//     "curl: (23) Failure writing output to destination"
//   - overlap on salt-call's per-minion execution lock, which manifests as:
//     "The function "state.apply" is running as PID N and was started at..."
//
// The mutex is non-reentrant — do not nest HostLockFor calls within an
// already-held critical section.
var hostLocks sync.Map

// HostLockFor returns the per-host *sync.Mutex for serializing Salt
// operations against a given target. Resource implementations should hold
// the lock from before EnsureVersion through after Apply/Test so the entire
// sequence runs atomically against the host.
func HostLockFor(host string) *sync.Mutex {
	m, _ := hostLocks.LoadOrStore(host, &sync.Mutex{})
	return m.(*sync.Mutex)
}
