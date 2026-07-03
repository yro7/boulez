package kernel

import (
	"claude-squad/config"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// PlanState is the lifecycle state of an orchestrator's plan. Persisted so
// an orchestrator can resume after a cs2 restart (the whole point of the
// plan store: a long-running orchestration survives a daemon bounce).
type PlanState string

const (
	PlanRunning  PlanState = "running"  // workers spawned, not all done
	PlanMerging  PlanState = "merging"  // merge in progress
	PlanDone     PlanState = "done"     // all workers done + merged
	PlanFailed   PlanState = "failed"   // a step failed irrecoverably
)

// MergeTarget is one merge an orchestrator will perform once its workers are
// done. An orchestrator may have several (merge worker-branches into repo A's
// integration, others into repo B).
type MergeTarget struct {
	Repo   string   `json:"repo"`
	Branch string   `json:"branch"`
	Sources []string `json:"sources"`
}

// OrchestratorPlan is the persisted state of an orchestrator's supervision:
// which workers it spawned, what it intends to merge, and where it is in the
// lifecycle. Stored under ~/.cs2/orchestrators/<id>/plan.json.
//
// The kernel owns this store; the orchestrator instance (an LLM, Shape B)
// consumes the control API to drive the plan. This is the resumability
// substrate: on restart, the kernel reloads plans in Running/Merging state
// and re-exposes them so the orchestrator can pick up where it left off.
type OrchestratorPlan struct {
	ID           string       `json:"id"`
	WorkerIDs    []string     `json:"worker_ids"`
	MergeTargets []MergeTarget `json:"merge_targets"`
	State        PlanState    `json:"state"`
}

// planStore persists OrchestratorPlans to disk. It is the kernel's
// persistence layer for orchestration state, parallel to Storage (which
// persists instances). Plans live in their own directory so a plan's data
// is co-located with the orchestrator's control dir.
type planStore struct {
	mu sync.Mutex
}

var plans = &planStore{}

// orchestratorsDir returns ~/.cs2/orchestrators/.
func orchestratorsDir() (string, error) {
	return config.OrchestratorsDir()
}

// planPath returns the path to an orchestrator's plan.json.
func planPath(id string) (string, error) {
	dir, err := orchestratorsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id, "plan.json"), nil
}

// LoadPlan reads an orchestrator's plan from disk. Returns os.ErrNotExist-
// compatible error if the plan file is absent (a fresh orchestrator that has
// not been persisted yet).
func LoadPlan(id string) (*OrchestratorPlan, error) {
	return plans.load(id)
}

func (s *planStore) load(id string) (*OrchestratorPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := planPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p OrchestratorPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	return &p, nil
}

// SavePlan persists an orchestrator's plan. Called by the kernel on every
// plan mutation (spawn a worker → add to WorkerIDs; merge → transition State).
func SavePlan(p *OrchestratorPlan) error {
	return plans.save(p)
}

func (s *planStore) save(p *OrchestratorPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == nil || p.ID == "" {
		return fmt.Errorf("plan: ID is required")
	}
	path, err := planPath(p.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create plan dir: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ListPlans returns all persisted plans, sorted by ID for stable output.
// Used by the kernel on startup to find resumable plans.
func ListPlans() ([]*OrchestratorPlan, error) {
	return plans.list()
}

func (s *planStore) list() ([]*OrchestratorPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := orchestratorsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no orchestrators yet
		}
		return nil, err
	}
	var plans []*OrchestratorPlan
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "plan.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // a control dir without a plan yet — skip
		}
		var p OrchestratorPlan
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		plans = append(plans, &p)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].ID < plans[j].ID })
	return plans, nil
}
