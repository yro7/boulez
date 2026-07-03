package kernel

import (
	"claude-squad/session"
	"claude-squad/session/git"
)

// InstanceSummary is the list-view projection of an instance: enough to
// identify and triage it, without the heavy diff/log payload.
type InstanceSummary struct {
	ID       string
	Kind     session.Kind
	Status   session.Status
	Title    string
	Repo     string
	Branch   string
	Program  string
	Host     string
	Updated  string // RFC3339 of the instance's UpdatedAt
}

// InstanceDetail is the full projection an orchestrator uses to decide: the
// summary plus the diff stats and the tmux scrollback (best-effort).
type InstanceDetail struct {
	InstanceSummary
	Diff    *git.DiffStats
	Log     string // tmux scrollback; empty when unavailable
}

// ListFilter narrows ListInstances. Zero-value filter = return everything.
// Because KindWorker and Status Running are zero values, the matcher cannot
// distinguish "filter on Worker" from "no filter" via zero-checks; filters
// that need to match a zero-valued Kind/Status must set the corresponding
// "Set" bool. The helpers (FilterByKind, FilterByStatus) construct these
// correctly.
type ListFilter struct {
	kind    session.Kind
	status  session.Status
	repo    string
	kindSet bool
	statusSet bool
}

// FilterByKind returns a filter that narrows by Kind.
func FilterByKind(k session.Kind) ListFilter {
	return ListFilter{kind: k, kindSet: true}
}

// FilterByStatus returns a filter that narrows by Status.
func FilterByStatus(s session.Status) ListFilter {
	return ListFilter{status: s, statusSet: true}
}

// FilterByRepo returns a filter that narrows by repo name.
func FilterByRepo(repo string) ListFilter {
	return ListFilter{repo: repo}
}

// matches reports whether a summary passes the filter. A zero filter means
// "return everything".
func (f ListFilter) matches(s InstanceSummary) bool {
	if f.kindSet && s.Kind != f.kind {
		return false
	}
	if f.statusSet && s.Status != f.status {
		return false
	}
	if f.repo != "" && s.Repo != f.repo {
		return false
	}
	return true
}

// summarize projects an instance to its list-view summary.
func summarize(inst *session.Instance) InstanceSummary {
	var repo string
	// RepoName requires a started instance with a bound worktree; the kernel
	// may hold instances mid-construction (e.g. an orchestrator's headless
	// worktree has no repo name). Deref defensively.
	if inst.Started() {
		if name, err := inst.RepoName(); err == nil {
			repo = name
		}
	}
	return InstanceSummary{
		ID:      inst.GetID(),
		Kind:    inst.Kind(),
		Status:  inst.Status,
		Title:   inst.Title,
		Repo:    repo,
		Branch:  inst.Branch,
		Program: inst.Program,
		Host:    inst.Host().Name(),
		Updated: inst.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// detail projects an instance to its full detail (summary + diff + log).
func detail(inst *session.Instance) InstanceDetail {
	d := InstanceDetail{InstanceSummary: summarize(inst)}
	d.Diff = inst.GetDiffStats()
	if log, err := inst.PreviewFullHistory(); err == nil {
		d.Log = log
	}
	return d
}
