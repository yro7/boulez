package kernel

import (
	"claude-squad/log"
	"claude-squad/session"
	"sync"
)

// instances is the in-memory fleet the kernel owns. It is loaded lazily from
// storage on first access and kept in sync as the kernel mutates. The kernel
// is the single writer, so no other goroutine touches this slice directly.
type instances struct {
	mu   sync.Mutex
	list []*session.Instance
	// loaded is false until the first load from storage. Lets the kernel
	// distinguish "empty fleet" from "not yet loaded".
	loaded bool
}

// loadLocked populates the in-memory list from storage if not already loaded.
// Caller must NOT hold the storage's lock; this calls Storage.loadInstances.
func (is *instances) loadLocked(storage *Storage) {
	if is.loaded || storage == nil {
		is.loaded = true
		return
	}
	loaded, err := storage.loadInstances()
	if err != nil {
		log.ErrorLog.Printf("kernel: failed to load instances: %v", err)
	}
	if loaded != nil {
		is.list = append(is.list, loaded...)
	}
	is.loaded = true
}

// find returns the instance with the given ID, or nil/false.
func (is *instances) find(id string) (*session.Instance, bool) {
	for _, inst := range is.list {
		if inst.GetID() == id {
			return inst, true
		}
	}
	return nil, false
}

// add registers a new instance.
func (is *instances) add(inst *session.Instance) {
	is.list = append(is.list, inst)
}

// remove drops the instance with the given ID.
func (is *instances) remove(id string) {
	for i, inst := range is.list {
		if inst.GetID() == id {
			is.list = append(is.list[:i], is.list[i+1:]...)
			return
		}
	}
}

// all returns a defensive copy of the list.
func (is *instances) all() []*session.Instance {
	out := make([]*session.Instance, len(is.list))
	copy(out, is.list)
	return out
}

// --- Kernel-internal accessors ---

// The Kernel embeds the instance store. To keep the public API clean, these
// helpers are unexported and used by the syscall methods.

func (k *Kernel) store() *instances { return k.instStore }

// instancesLocked returns the in-memory fleet, loading from storage on first
// access. Caller must hold k.mu.
func (k *Kernel) instancesLocked() []*session.Instance {
	if k.instStore == nil {
		k.instStore = &instances{}
	}
	if !k.instStore.loaded {
		k.instStore.loadLocked(k.storage)
	}
	return k.instStore.all()
}

// findLocked returns the instance with the given ID. Caller must hold k.mu.
func (k *Kernel) findLocked(id string) (*session.Instance, bool) {
	k.instancesLocked() // ensure loaded
	return k.instStore.find(id)
}

// InstanceByID returns the *session.Instance for the given ID. It is the only
// exported accessor for a live instance pointer, used by consumers that need
// to act on the instance directly (e.g. the orchestrator bootstrap calls
// SendPrompt on it). Returns ErrUnknownInstance if the ID is not in the fleet.
func (k *Kernel) InstanceByID(id string) (*session.Instance, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	inst, ok := k.findLocked(id)
	if !ok {
		return nil, ErrUnknownInstance{ID: id}
	}
	return inst, nil
}

// LiveInstances returns a defensive copy of the in-memory fleet, loading from
// storage on first access. It is the source of truth the daemon's poll loop
// consumes: the daemon must NOT keep its own separate slice of instances —
// the kernel is the single writer (invariant 1), and a second in-memory copy
// drifts (e.g. it never sees the orchestrator the kernel spawns, then the
// daemon's shutdown-save clobbers the kernel's persisted state).
//
// The returned pointers are the live instances the kernel owns; callers may
// observe them (poll status, update diff stats) but must not mutate fleet
// membership through them — that goes through the kernel syscalls.
func (k *Kernel) LiveInstances() []*session.Instance {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.instancesLocked()
}

func (k *Kernel) registerLocked(inst *session.Instance) {
	if k.instStore == nil {
		k.instStore = &instances{loaded: true}
	}
	k.instStore.add(inst)
}

// persist writes the current fleet to storage. Best-effort: errors are
// logged, not returned, because a save failure should not abort a successful
// mutation. Called outside the kernel lock to avoid holding it during I/O.
func (k *Kernel) persist(storage *Storage, _ *session.Instance) error {
	k.mu.Lock()
	all := k.instStore.all()
	k.mu.Unlock()
	if err := storage.saveInstances(all); err != nil {
		log.ErrorLog.Printf("kernel: failed to persist: %v", err)
		return err
	}
	return nil
}

// ReconcileLiveness probes the tmux liveness of every loaded instance and
// demotes any whose tmux session is definitively gone to Dead. It is the
// C4.4 boot reconciliation: after a daemon restart following a tmux crash,
// instances persisted as Running would otherwise show as a ghost "running"
// even though their tmux session no longer exists.
//
// Only a started, non-paused, non-dead instance is a candidate: a Paused
// instance intentionally has no live tmux session (Pause kills it), and an
// already-Dead instance is left alone. Demotion happens ONLY when tmux
// definitively reports the session absent (DoesSessionExist == false) — a
// timeout or tmux error must NOT demote, because a slow instance is not a
// dead one (spec: never demote on a timeout).
//
// Safe to call with no tmux available (e.g. in-memory test kernels): an
// instance with no tmuxSession handle is skipped, since there is nothing to
// probe.
func (k *Kernel) ReconcileLiveness() {
	k.mu.Lock()
	instances := k.instancesLocked()
	// Snapshot the candidates under the lock so the probe loop releases it
	// before shelling out to tmux (DoesSessionExist blocks on a subprocess).
	candidates := make([]*session.Instance, 0, len(instances))
	for _, inst := range instances {
		if !inst.Started() || inst.Paused() || inst.Status == session.Dead {
			continue
		}
		candidates = append(candidates, inst)
	}
	k.mu.Unlock()

	demoted := false
	for _, inst := range candidates {
		if inst.TmuxAlive() {
			continue
		}
		inst.SetStatus(session.Dead)
		log.WarningLog.Printf("kernel: instance %s demoted to Dead (tmux session gone)", inst.GetID())
		demoted = true
	}

	if demoted {
		storage := k.storage
		autosave := k.autosave
		if autosave && storage != nil {
			_ = k.persist(storage, nil)
		}
	}
}
