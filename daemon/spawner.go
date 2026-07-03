package daemon

import (
	"claude-squad/app"
	"claude-squad/cmd"
	"claude-squad/kernel"
	"claude-squad/session"
	"claude-squad/session/git"
)

// kernelSpawner adapts app.Spawn to the kernel.Spawner interface. It is the
// production spawner: real tmux session + real git worktree. The daemon wires
// it so the kernel's spawn_worker syscall creates live instances.
//
// This is the bridge between the kernel (pure Go, testable) and app.Spawn
// (tmux-coupled). The kernel never imports app; the daemon does the wiring.
type kernelSpawner struct{}

func (kernelSpawner) Spawn(opts kernel.SpawnOptions) (*session.Instance, error) {
	return app.Spawn(app.SpawnOptions{
		Repo:            opts.Repo,
		Branch:          opts.Branch,
		BranchMustExist: opts.BranchMustExist,
		Prompt:          opts.Prompt,
		Program:         opts.Program,
		Title:           opts.Title,
		Host:            opts.Host,
		Kind:            opts.Kind,
	})
}

// realMerger adapts git.NewMerger to the git.Merger interface the kernel
// consumes. Thin wrapper: production uses the local command executor; a
// remote repo's merges route over SSH via the host's executor in v2.
type realMerger struct{}

func (realMerger) Merge(repoPath, targetBranch string, sourceBranches []string, strategy git.Strategy) (git.MergeResult, error) {
	return git.NewMerger(cmd.MakeExecutor()).Merge(repoPath, targetBranch, sourceBranches, strategy)
}

// Compile-time checks that the adapters satisfy the kernel's interfaces.
var (
	_ kernel.Spawner = kernelSpawner{}
	_ git.Merger     = realMerger{}
)
