package kernel

import "fmt"

// ErrUnknownInstance is returned when a syscall addresses an ID the kernel
// has no record of. Typed so the transport can map it to an UNKNOWN_INSTANCE
// error code for the client.
type ErrUnknownInstance struct {
	ID string
}

func (e ErrUnknownInstance) Error() string {
	return fmt.Sprintf("kernel: unknown instance %q", e.ID)
}

// ErrWorkerCannotSpawn is the recursion guard: a Worker instance cannot
// spawn other instances. The topology is strictly two levels in v1 (an
// Orchestrator spawns Workers; a Worker spawns nothing).
type ErrWorkerCannotSpawn struct{}

func (ErrWorkerCannotSpawn) Error() string {
	return "kernel: a worker cannot spawn instances (topology is two-level)"
}

// ErrNestedOrchestrator is the second-level guard: in v1 an Orchestrator
// cannot spawn another Orchestrator (no super-orchestrator hierarchy yet).
// Lifting this is the documented extension point for the future
// super-orchestrator → n orchestrators → m workers topology.
type ErrNestedOrchestrator struct{}

func (ErrNestedOrchestrator) Error() string {
	return "kernel: an orchestrator cannot spawn another orchestrator (super-orchestrator hierarchy not yet supported)"
}

// ErrNonTopLevelLand is the topology guard for the Land syscall: only a
// top-level caller (`cs2 ctl` / TUI) may land onto a trunk. Workers and
// orchestrators must use Merge, which refuses trunks in depth. This mirrors
// the recursion guard's intent: the v1 topology forbids instances from
// touching the trunk.
type ErrNonTopLevelLand struct{}

func (ErrNonTopLevelLand) Error() string {
	return "kernel: only a top-level caller may land onto a trunk (workers and orchestrators must use Merge)"
}

// isKernelProtected reports whether branch is in the kernel-level protected
// set (the host repo's current branch + any extra the daemon injected). The
// comparison is case-insensitive to match git's branch name normalisation
// across platforms. The Merger defends main/master in depth separately.
func isKernelProtected(protected []string, branch string) bool {
	for _, p := range protected {
		if equalFoldBranch(p, branch) {
			return true
		}
	}
	return false
}

// equalFoldBranch compares branch names case-insensitively. git branch names
// are case-sensitive on disk but the protected-branch check is defensive —
// a caller shouldn't bypass it by capitalising "Main".
func equalFoldBranch(a, b string) bool {
	la, lb := []byte(a), []byte(b)
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		ca, cb := la[i], lb[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
