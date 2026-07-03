package overlay

// RepoSelector is a thin configuration of ListSelector for choosing a
// repository path at instance creation. It offers the known repos (from the
// registry) plus a free-text path input. The chosen path is validated by the
// caller (not here).
//
// All list behaviour (cursor, typing, submit, cancel, delete) lives in the
// embedded ListSelector; this type only adds repo-specific accessors.
type RepoSelector struct {
	*ListSelector
}

// repoHints is the help line shown at the bottom of the repo selector.
const repoHints = "type to filter · ↑↓ move · enter select · esc cancel"

// NewRepoSelector creates a selector pre-populated with the given known repos.
func NewRepoSelector(repos []string) *RepoSelector {
	items := make([]selectorItem, 0, len(repos))
	for _, repo := range repos {
		items = append(items, selectorItem{label: repo, value: repo, deletable: true})
	}
	return &RepoSelector{ListSelector: NewListSelector("Select repository", items, true, "Path: ", repoHints)}
}

// SetRepos replaces the offered repo list, preserving the cursor and free-text
// input when possible. Used to narrow the list after an async host-aware
// filter lands: the selector starts with the full registry and drops entries
// that don't exist on the chosen host once probed. Cursor is clamped to the
// free-path row (last) if it now points past the end.
func (r *RepoSelector) SetRepos(repos []string) {
	items := make([]selectorItem, 0, len(repos))
	for _, repo := range repos {
		items = append(items, selectorItem{label: repo, value: repo, deletable: true})
	}
	r.SetItems(items)
}

// SelectedPath returns the path chosen by the user. For a known-repo row it is
// that repo's path; for the free-path row it is the typed text (which may be
// empty). Returns "" if nothing was selected or the overlay was canceled.
func (r *RepoSelector) SelectedPath() string { return r.SelectedValue() }

// IsFreePath reports whether the selection came from the free-text input (the
// caller uses this to decide whether to register the path in the registry).
func (r *RepoSelector) IsFreePath() bool { return r.IsFreeValue() }

// isFreePathRow reports whether a submit would resolve to a free-text path
// (i.e. no item is highlighted). Kept for compatibility with the fused model.
func (r *RepoSelector) isFreePathRow() bool { return r.isFreeRow() }
