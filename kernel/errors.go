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
