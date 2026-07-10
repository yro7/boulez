package session

import (
	"time"
)

// InstanceData represents the serializable data of an Instance
type InstanceData struct {
	// ID is the stable, immutable instance handle. May be empty for
	// instances persisted before this field existed; FromInstanceData
	// backfills one in that case.
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Status    Status    `json:"status"`
	Height    int       `json:"height"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AutoYes   bool      `json:"auto_yes"`

	// Kind is the instance role (worker vs orchestrator). Persisted so the
	// role survives a restart. Defaults to KindWorker for legacy data (zero
	// value), which is the only role that existed before this field.
	Kind Kind `json:"kind"`

	Program   string          `json:"program"`
	Host      string          `json:"host"`
	Worktree  GitWorktreeData `json:"worktree"`
	DiffStats DiffStatsData   `json:"diff_stats"`

	// Landed is the persisted, wire-visible mirror of the TUI-only "landed"
	// hint: it is set true by the kernel after a successful merge/land for
	// every instance whose branch was a source of the merge. Unlike the
	// TUI's view-only hint (which is cleared on Running→Ready and never
	// crosses the wire), this field is what `boulez ctl list_instances` /
	// `get_instance` expose so non-TUI consumers can see that an instance's
	// branch has been merged into the target trunk. The TUI does NOT sync
	// this field back into its view handles (reconcileFleet only refreshes
	// Status/AutoYes), so the TUI's own Running→Ready clear is not fought by
	// a fleet refresh.
	Landed bool `json:"landed"`

	// ArchivedAt is the time the instance was soft-deleted (archived) via the
	// kernel's Archive syscall; the zero time means the instance is live.
	// The daemon's ReapArchived sweep truly destroys the instance once
	// now - ArchivedAt > retention (default 24h). Persisted so the retention
	// survives a daemon restart.
	ArchivedAt time.Time `json:"archived_at"`
}

// GitWorktreeData represents the serializable data of a GitWorktree
type GitWorktreeData struct {
	RepoPath         string `json:"repo_path"`
	WorktreePath     string `json:"worktree_path"`
	SessionName      string `json:"session_name"`
	BranchName       string `json:"branch_name"`
	BaseCommitSHA    string `json:"base_commit_sha"`
	IsExistingBranch bool   `json:"is_existing_branch"`
}

// DiffStatsData represents the serializable data of a DiffStats
type DiffStatsData struct {
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Content string `json:"content"`
}

// NOTE: fleet persistence (Storage) lives in the kernel package, NOT here.
// session knows the data shape (InstanceData + FromInstanceData +
// ToInstanceData); the kernel knows when the fleet is persisted, because
// the kernel is the single writer (invariant 1). The former session.Storage
// write methods were moved to kernel.Storage (unexported) in C4.3 so that
// app/ cannot reach SaveInstances even at compile time.
