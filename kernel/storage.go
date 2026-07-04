package kernel

import (
	"claude-squad/config"
	"claude-squad/session"
	"encoding/json"
	"fmt"
)

// Storage is the kernel's persistence layer for the fleet. It is the ONLY
// writer of fleet state (invariant 1: the kernel is the single writer), so
// its write methods are UNEXPORTED — packages outside kernel (notably the
// TUI in app/) cannot reach saveInstances even if they tried. This is the
// C4.3 compile-time guarantee, belt-and-braces over the behavioural fix in
// C3.5 (which removed the TUI's SaveInstances call sites).
//
// The shape mirrors the former session.Storage, but the writer lives here
// on the kernel package because that is where the single-writer authority
// lives. session keeps only the data shape (InstanceData + FromInstanceData
// + ToInstanceData): it knows how an instance serializes, the kernel knows
// when (and only when) the fleet is persisted.
type Storage struct {
	state config.InstanceStorage
}

// NewStorage wraps the on-disk instance store. The kernel is the sole
// constructor; app/ does not construct or hold a *Storage.
func NewStorage(state config.InstanceStorage) *Storage {
	return &Storage{state: state}
}

// saveInstances persists the fleet. Only started instances are written
// (matching the historical contract: a not-yet-started draft is not
// persisted). Unexported: only the kernel may write the fleet.
func (s *Storage) saveInstances(instances []*session.Instance) error {
	data := make([]session.InstanceData, 0, len(instances))
	for _, inst := range instances {
		if inst.Started() {
			data = append(data, inst.ToInstanceData())
		}
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}
	return s.state.SaveInstances(jsonData)
}

// loadInstances reconstructs the fleet from disk. Unexported: only the
// kernel loads the fleet (lazily, on first access via instancesLocked).
func (s *Storage) loadInstances() ([]*session.Instance, error) {
	jsonData := s.state.GetInstances()

	var instancesData []session.InstanceData
	if err := json.Unmarshal(jsonData, &instancesData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instances: %w", err)
	}

	instances := make([]*session.Instance, len(instancesData))
	for i, data := range instancesData {
		inst, err := session.FromInstanceData(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create instance %s: %w", data.Title, err)
		}
		instances[i] = inst
	}
	return instances, nil
}
