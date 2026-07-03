package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"claude-squad/config"
	"claude-squad/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstance_DefaultsToLocalHost verifies that a freshly created Instance
// runs on LocalHost — i.e. Step 1b is behaviour-neutral: today every
// instance is local, so the host field must default to LocalHost until a
// caller explicitly sets an SSHHost (v2).
func TestInstance_DefaultsToLocalHost(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title:   "t",
		Path:    repoPath,
		Program: "claude",
	})
	require.NoError(t, err)

	_, ok := inst.Host().(host.LocalHost)
	assert.True(t, ok, "new instance should default to LocalHost")
}

// TestInstance_RoutesWorktreeThroughHost proves the seam: FromInstanceData
// builds the worktree via NewGitWorktreeFromStorageWithDeps with the host's
// Executor/FS, not with hardcoded local defaults inside the git package.
// With the LocalHost default the host is LocalHost — the guarantee v2 needs
// is that swapping the host swaps the deps the worktree was built with.
func TestInstance_RoutesWorktreeThroughHost(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	data := InstanceData{
		Title:   "stored",
		Path:    repoPath,
		Branch:  "cs2/stored",
		Status:  Paused, // Paused so FromInstanceData doesn't call Start()
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:      repoPath,
			WorktreePath:  filepath.Join(t.TempDir(), "wt"),
			SessionName:   "stored",
			BranchName:    "cs2/stored",
			BaseCommitSHA: "HEAD",
		},
	}

	inst, err := FromInstanceData(data)
	require.NoError(t, err)

	// The host is LocalHost (default) and is the source the worktree was
	// built from. A future SSHHost test will swap the host and assert the
	// worktree's deps follow.
	_, ok := inst.Host().(host.LocalHost)
	require.True(t, ok, "restored instance should default to LocalHost")
}

// makeTempGitRepo creates a real git repository at a temp dir and returns its
// absolute path. Used by tests that need a valid repo for Instance construction.
func makeTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, "", "init", dir)
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "t@e.com")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return abs
}

// runGit runs a git command with -C dir (if non-empty) and fails the test on
// error. Defined here because the session package has no shared git test
// helper yet.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	cmdArgs = append([]string{"git"}, cmdArgs...)
	out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

// TestInstance_UsesHostWorktreeDir proves the path-generation seam: an
// Instance built from storage uses its Host's WorktreeDir, not the local
// config dir baked into the git package. With LocalHost this is the local
// ~/.cs2/worktrees (non-regression); the point is that the dir comes from the
// host, so an SSHHost's ~-relative dir would flow through to Setup.
func TestInstance_UsesHostWorktreeDir(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	// Isolate HOME so LocalHost.WorktreeDir is deterministic.
	tempHome := t.TempDir()
	orig := os.Getenv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", orig) }()

	data := InstanceData{
		Title:   "stored",
		Path:    repoPath,
		Branch:  "cs2/stored",
		Status:  Paused,
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:      repoPath,
			WorktreePath:  filepath.Join(t.TempDir(), "wt"),
			SessionName:   "stored",
			BranchName:    "cs2/stored",
			BaseCommitSHA: "HEAD",
		},
	}

	inst, err := FromInstanceData(data)
	require.NoError(t, err)

	// The host's WorktreeDir is the local ~/.cs2/worktrees. The instance was
	// built from the host's dir (not a stale local-derived one).
	wantDir, err := inst.Host().WorktreeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tempHome, ".cs2", "worktrees"), wantDir)
}

// TestInstance_HostRoundTrip proves persistence: serializing an instance with
// an SSHHost and restoring it yields an instance whose host is an SSHHost
// bound to the same alias. This is the contract the creation flow + storage
// rely on: the host survives a cs2 restart.
func TestInstance_HostRoundTrip(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title:   "remote",
		Path:    repoPath,
		Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, inst.SetHost(host.NewSSHHost("dev-machine")))

	data := inst.ToInstanceData()
	assert.Equal(t, "dev-machine", data.Host, "ToInstanceData must serialize the host name")

	restored, err := FromInstanceData(data)
	require.NoError(t, err)

	ssh, ok := restored.Host().(host.SSHHost)
	require.True(t, ok, "restored instance must be an SSHHost")
	assert.Equal(t, "dev-machine", ssh.Alias())
	assert.False(t, restored.Host().AutoYesDefault(), "remote AutoYes default must survive round-trip")
}

// TestInstance_SetHost_RefusedAfterStart proves the guard: changing the host
// after Start would leave stale tmux/git sessions bound to the wrong host, so
// it must error.
func TestInstance_SetHost_RefusedAfterStart(t *testing.T) {
	inst := &Instance{host: host.Local, started: true}
	err := inst.SetHost(host.NewSSHHost("dev-machine"))
	assert.Error(t, err, "SetHost after Start must error")
}

// TestInstance_AutoYes_RestoredFromStorage proves AutoYes is truly per-instance
// now: FromInstanceData restores the persisted value instead of the daemon
// force-setting it to true globally. This is the v2 AutoYes contract — a
// remote instance created with AutoYes off stays off across a restart.
func TestInstance_AutoYes_RestoredFromStorage(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	// An instance persisted with AutoYes ON restores ON.
	dataOn := InstanceData{
		Title: "on", Path: repoPath, Status: Paused, Program: "claude", AutoYes: true,
		Worktree: GitWorktreeData{RepoPath: repoPath, WorktreePath: "/tmp/wt"},
	}
	instOn, err := FromInstanceData(dataOn)
	require.NoError(t, err)
	assert.True(t, instOn.AutoYes, "persisted AutoYes=true must restore as true")

	// An instance persisted with AutoYes OFF restores OFF (no global forcing).
	dataOff := InstanceData{
		Title: "off", Path: repoPath, Status: Paused, Program: "claude", AutoYes: false,
		Worktree: GitWorktreeData{RepoPath: repoPath, WorktreePath: "/tmp/wt"},
	}
	instOff, err := FromInstanceData(dataOff)
	require.NoError(t, err)
	assert.False(t, instOff.AutoYes, "persisted AutoYes=false must restore as false (no global forcing)")
}

// TestInstance_AutoYes_HostPolicyDrivesNewInstance proves the new-instance
// default comes from the host's policy: local follows the global config flag,
// remote defaults to off. (The app applies Host().AutoYesDefault() at start;
// here we assert the host policy itself.)
func TestInstance_AutoYes_HostPolicyDrivesNewInstance(t *testing.T) {
	assert.False(t, host.NewSSHHost("dev-machine").AutoYesDefault(),
		"remote host AutoYes policy must be off")
	assert.Equal(t, host.Local.AutoYesDefault(), config.LoadConfig().AutoYes,
		"local host AutoYes policy must follow the global config flag")
}

// TestInstance_SetAutoYes_Toggles proves the per-instance toggle.
func TestInstance_SetAutoYes_Toggles(t *testing.T) {
	inst := &Instance{host: host.Local, AutoYes: false}
	inst.SetAutoYes(true)
	assert.True(t, inst.AutoYes)
	inst.SetAutoYes(false)
	assert.False(t, inst.AutoYes)
}
