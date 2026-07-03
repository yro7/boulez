package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAPI is an in-memory API for testing Ensure without tmux, the kernel, or
// an LLM. It records every call so tests can assert the bootstrap behaviour.
type fakeAPI struct {
	orchs     []Instance
	fleet     []Instance
	spawned   []string // programs passed to SpawnOrchestrator
	spawnID   string
	spawnErr  error
	prompts   []promptCall
	promptErr error

	// alive is the set of instance IDs whose tmux session is considered live.
	// Defaults to "every orchestrator is alive" when nil, so the restart path
	// (an existing live orchestrator) works without per-test setup.
	alive    map[string]bool
	killed   []string // IDs passed to Kill
	killErr  error
}

type promptCall struct {
	id, prompt string
}

func (f *fakeAPI) ListInstances() []Instance { return f.fleet }

func (f *fakeAPI) ListOrchestrators() []Instance { return f.orchs }

func (f *fakeAPI) SpawnOrchestrator(program string) (string, error) {
	f.spawned = append(f.spawned, program)
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	id := f.spawnID
	if id == "" {
		id = "orch-new"
	}
	// Reflect the new orchestrator into the fleet views so a subsequent
	// ListInstances (the injection) sees it. The new instance is alive.
	if f.alive == nil {
		f.alive = map[string]bool{}
	}
	f.alive[id] = true
	f.orchs = append(f.orchs, Instance{ID: id, Kind: "orchestrator"})
	f.fleet = append(f.fleet, Instance{ID: id, Kind: "orchestrator"})
	return id, nil
}

func (f *fakeAPI) SendPrompt(id, prompt string) error {
	f.prompts = append(f.prompts, promptCall{id, prompt})
	return f.promptErr
}

// IsAlive reports whether the instance is in the alive set. When the alive
// map is nil, every existing orchestrator is considered alive (the common
// restart path). An ID not in a non-nil map is dead.
func (f *fakeAPI) IsAlive(id string) bool {
	if f.alive == nil {
		return true
	}
	return f.alive[id]
}

// Kill records the eviction and drops the instance from the fleet views.
func (f *fakeAPI) Kill(id string) error {
	f.killed = append(f.killed, id)
	if f.killErr != nil {
		return f.killErr
	}
	f.orchs = removeInstance(f.orchs, id)
	f.fleet = removeInstance(f.fleet, id)
	return nil
}

func removeInstance(in []Instance, id string) []Instance {
	out := in[:0]
	for _, x := range in {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return out
}

// TestEnsure_FirstCreation_SpawnsAndInjectsOnce is the core contract: on a
// fresh fleet (no orchestrator), Ensure spawns one, writes the context file,
// and injects the fleet snapshot exactly once.
func TestEnsure_FirstCreation_SpawnsAndInjectsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	worker := Instance{ID: "w1", Kind: "worker", Status: "running", Title: "fix-bug"}
	api := &fakeAPI{
		fleet:   []Instance{worker},
		spawnID: "orch-1",
	}

	if _, err := Ensure(api, "pi"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := len(api.spawned); got != 1 {
		t.Fatalf("expected 1 spawn, got %d", got)
	}
	if api.spawned[0] != "pi" {
		t.Errorf("spawned program = %q, want %q", api.spawned[0], "pi")
	}
	if len(api.prompts) != 1 {
		t.Fatalf("expected exactly 1 injected prompt, got %d", len(api.prompts))
	}
	if api.prompts[0].id != "orch-1" {
		t.Errorf("prompt sent to %q, want orch-1", api.prompts[0].id)
	}
	// The injected prompt must carry the fleet snapshot.
	if !strings.Contains(api.prompts[0].prompt, "w1") {
		t.Errorf("injected prompt does not contain the fleet snapshot:\n%s", api.prompts[0].prompt)
	}
	if !strings.Contains(api.prompts[0].prompt, "ORCHESTRATOR.md") {
		t.Errorf("injected prompt does not point at ORCHESTRATOR.md")
	}

	// The context file must have been written to the control dir.
	dir, err := ControlDir("orch-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dir + "/" + ContextFileName)
	if err != nil {
		t.Fatalf("context file not written: %v", err)
	}
	if !strings.Contains(string(got), "orch-1") {
		t.Errorf("context file does not mention the orchestrator ID")
	}
}

// TestEnsure_Restart_DoesNotReinject proves the idempotent path: when an
// orchestrator already exists, Ensure refreshes the context file but does
// NOT spawn or inject a prompt again.
func TestEnsure_Restart_DoesNotReinject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	existing := Instance{ID: "orch-existing", Kind: "orchestrator", Status: "running"}
	api := &fakeAPI{
		orchs: []Instance{existing},
		fleet: []Instance{existing},
	}

	if _, err := Ensure(api, "pi"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if len(api.spawned) != 0 {
		t.Errorf("restart must not spawn, got %d spawns", len(api.spawned))
	}
	if len(api.prompts) != 0 {
		t.Errorf("restart must not inject a prompt, got %d prompts", len(api.prompts))
	}
	// But the context file IS refreshed (so docs stay current across upgrades).
	dir, err := ControlDir("orch-existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(dir + "/" + ContextFileName); err != nil {
		t.Errorf("restart must refresh the context file: %v", err)
	}
}

// TestEnsure_DeadOrchestrator_Respawns proves the self-heal: when an
// orchestrator record exists but its tmux session is dead (Ctrl+D killed it),
// Ensure evicts the dead record and spawns a fresh live one — re-injecting the
// fleet snapshot. This is the fix for the "closed the instance, won't open
// again until I restart" symptom: the dead record must not be mistaken for a
// live instance 0.
func TestEnsure_DeadOrchestrator_Respawns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dead := Instance{ID: "orch-dead", Kind: "orchestrator", Status: "running", Title: "orchestrator"}
	api := &fakeAPI{
		orchs:   []Instance{dead},
		fleet:   []Instance{dead},
		spawnID: "orch-fresh",
		alive:   map[string]bool{"orch-dead": false}, // record exists but tmux is dead
	}

	id, err := Ensure(api, "pi")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if id != "orch-fresh" {
		t.Fatalf("expected respawned id orch-fresh, got %s", id)
	}
	// The dead record must have been evicted.
	if len(api.killed) != 1 || api.killed[0] != "orch-dead" {
		t.Fatalf("expected dead orchestrator evicted, got killed=%v", api.killed)
	}
	// A fresh one must have been spawned and injected exactly once.
	if len(api.spawned) != 1 || api.spawned[0] != "pi" {
		t.Fatalf("expected one spawn of pi, got %v", api.spawned)
	}
	if len(api.prompts) != 1 || api.prompts[0].id != "orch-fresh" {
		t.Fatalf("expected one injection to orch-fresh, got %v", api.prompts)
	}
	// The fleet view must now hold only the fresh orchestrator (no lingering dead slot).
	if len(api.orchs) != 1 || api.orchs[0].ID != "orch-fresh" {
		t.Fatalf("fleet should hold only the fresh orchestrator, got %v", api.orchs)
	}
}

// TestEnsureLive_LiveOrchestrator_NoContextRewrite proves the cheap periodic
// probe does no disk I/O when instance 0 is healthy: no context-file rewrite,
// no spawn, no injection. This is what makes it safe to call every poll tick.
func TestEnsureLive_LiveOrchestrator_NoContextRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	existing := Instance{ID: "orch-live", Kind: "orchestrator", Status: "running"}
	api := &fakeAPI{
		orchs: []Instance{existing},
		fleet: []Instance{existing},
		alive: map[string]bool{"orch-live": true},
	}

	// Pre-write the context file so we can detect a rewrite via mtime.
	if err := WriteContextFile("orch-live"); err != nil {
		t.Fatal(err)
	}
	dir, _ := ControlDir("orch-live")
	info, err := os.Stat(filepath.Join(dir, ContextFileName))
	if err != nil {
		t.Fatal(err)
	}
	firstMod := info.ModTime()

	// Sleep a hair so a rewrite would produce a strictly-newer mtime.
	time.Sleep(20 * time.Millisecond)

	if _, err := EnsureLive(api, "pi"); err != nil {
		t.Fatalf("EnsureLive: %v", err)
	}
	if len(api.spawned) != 0 || len(api.prompts) != 0 {
		t.Fatalf("live probe must not spawn/inject, got spawned=%v prompts=%v", api.spawned, api.prompts)
	}
	info, err = os.Stat(filepath.Join(dir, ContextFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMod) {
		t.Fatalf("EnsureLive must not rewrite context file on the live path")
	}
}

// TestEnsureLive_DeadOrchestrator_Respawns proves the periodic probe respawns
// a dead instance 0, reusing the same self-heal path as the startup Ensure.
func TestEnsureLive_DeadOrchestrator_Respawns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dead := Instance{ID: "orch-dead", Kind: "orchestrator", Status: "running", Title: "orchestrator"}
	api := &fakeAPI{
		orchs:   []Instance{dead},
		fleet:   []Instance{dead},
		spawnID: "orch-fresh",
		alive:   map[string]bool{"orch-dead": false},
	}

	id, err := EnsureLive(api, "pi")
	if err != nil {
		t.Fatalf("EnsureLive: %v", err)
	}
	if id != "orch-fresh" {
		t.Fatalf("expected orch-fresh, got %s", id)
	}
	if len(api.killed) != 1 || api.killed[0] != "orch-dead" {
		t.Fatalf("expected dead evicted, got killed=%v", api.killed)
	}
	if len(api.spawned) != 1 || len(api.prompts) != 1 {
		t.Fatalf("expected one spawn + one injection, got spawned=%v prompts=%v", api.spawned, api.prompts)
	}
	// The context file must be written by the respawn (spawnFresh writes it).
	dir, _ := ControlDir("orch-fresh")
	if _, err := os.Stat(filepath.Join(dir, ContextFileName)); err != nil {
		t.Fatalf("respawn must write context file: %v", err)
	}
}

// TestEnsure_SpawnErrorPropagates proves a spawn failure aborts Ensure and
// does not inject.
func TestEnsure_SpawnErrorPropagates(t *testing.T) {
	api := &fakeAPI{spawnErr: errors.New("tmux down")}

	_, err := Ensure(api, "pi")
	if err == nil || !strings.Contains(err.Error(), "tmux down") {
		t.Fatalf("expected spawn error to propagate, got %v", err)
	}
	if len(api.prompts) != 0 {
		t.Errorf("must not inject on spawn failure, got %d prompts", len(api.prompts))
	}
}

// TestEnsure_PromptErrorPropagates proves a SendPrompt failure surfaces (the
// orchestrator is started + context written, but the injection failed).
func TestEnsure_PromptErrorPropagates(t *testing.T) {
	api := &fakeAPI{spawnID: "orch-1", promptErr: errors.New("pane gone")}

	_, err := Ensure(api, "pi")
	if err == nil || !strings.Contains(err.Error(), "pane gone") {
		t.Fatalf("expected prompt error to propagate, got %v", err)
	}
}

// TestRenderFleet_Empty proves the empty-fleet case renders a clear placeholder
// (the orchestrator must not see an empty string and think the tool failed).
func TestRenderFleet_Empty(t *testing.T) {
	got := RenderFleet(nil)
	if !strings.Contains(got, "no instances") {
		t.Errorf("empty fleet render should say so, got %q", got)
	}
}

// TestContextContent_DocumentsToolSurface proves the context file documents
// every syscall the agent can call. This is a lightweight DRY pin: if a syscall
// is added to the CLI, this test reminds you to document it here.
func TestContextContent_DocumentsToolSurface(t *testing.T) {
	doc := ContextContent("orch-1")
	want := []string{
		"list_instances",
		"spawn_worker",
		"get_instance",
		"send_prompt",
		"pause",
		"resume",
		"kill",
		"merge",
		"PROTECTED_BRANCH",
		"BRANCH_NOT_FOUND",
		"UNKNOWN_INSTANCE",
		"orch-1",
	}
	for _, w := range want {
		if !strings.Contains(doc, w) {
			t.Errorf("context file missing %q", w)
		}
	}
}
