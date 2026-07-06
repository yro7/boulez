package cli

import (
	"fmt"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/ideimport"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/repo"
	"strings"

	"github.com/spf13/cobra"
)

var (
	repoImportIDE    string
	repoImportDryRun bool
)

var RepoImportCmd = &cobra.Command{
	Use:   "repo-import",
	Short: "Import recently-opened folders from IDE state into the repo registry",
	Long: `Scan VS Code and VS Code-based forks (Cursor, Windsurf, Antigravity, VSCodium,
PearAI, Void, Trae) for recently opened folders, keep only git repositories, and
add them to the boulez repo registry.

This is a one-shot, manual import — boulez never reads IDE state automatically, so
a format change in an IDE's storage.json never affects normal operation.

Use --dry-run to preview what would be imported without writing.`,
	RunE: runRepoImport,
}

// NewRepoImportCmd returns the `repo-import` command with its flags wired.
// It is constructed lazily (rather than a package-level var) so callers can
// attach it to an arbitrary root command without an init() side effect.
func NewRepoImportCmd() *cobra.Command {
	RepoImportCmd.Flags().StringVar(&repoImportIDE, "ide", "",
		"Restrict the scan to one IDE (one of: vscode, cursor, windsurf, antigravity, vscodium, pearai, void, trae)")
	RepoImportCmd.Flags().BoolVar(&repoImportDryRun, "dry-run", false,
		"List what would be imported without writing to the registry")
	return RepoImportCmd
}

func runRepoImport(cmd *cobra.Command, args []string) error {
	log.Initialize(false)
	log.SetPrintPathOnClose(true) // human-facing: surface log path on exit
	defer log.Close()

	// D12: NewImporter validates --ide before any scan (fail-fast).
	imp, err := ideimport.NewImporter(repoImportIDE)
	if err != nil {
		return err
	}
	found, warnings, err := imp.Scan()
	if err != nil {
		return err
	}

	// D16: surface corrupt-storage warnings (with cause) before the summary.
	for _, w := range warnings {
		fmt.Printf("warning: %s storage.json unreadable: %s, skipped\n", w.IDE, w.Cause)
	}

	// D17: nothing discovered is a normal state, not an error.
	if len(found) == 0 {
		fmt.Println("No IDEs found. Nothing to import.")
		return nil
	}

	// D19: load the registry even in dry-run (read-only) to distinguish new
	// from already known.
	reg, err := repo.NewRegistry()
	if err != nil {
		return err
	}

	if repoImportDryRun {
		fmt.Print(formatDryRunSummary(found, reg))
		return nil
	}

	// D4: write path emits only the one-line summary.
	newCount, knownCount := 0, 0
	for _, f := range found {
		wasNew := !reg.Contains(f.Path, host.LocalAlias)
		if err := reg.Add(f.Path, host.LocalAlias); err != nil {
			return err
		}
		if wasNew {
			newCount++
		} else {
			knownCount++
		}
	}
	fmt.Printf("Imported %d repos (%d new, %d already known).\n", len(found), newCount, knownCount)
	return nil
}

// formatDryRunSummary renders the --dry-run preview: a header line with the
// new/known split, then one line per discovered repo tagged with its source
// IDE. Reads the registry read-only (D19) to distinguish new from already
// known without mutating anything.
func formatDryRunSummary(found []ideimport.FoundRepo, reg *repo.Registry) string {
	var b strings.Builder
	newCount, knownCount := 0, 0
	for _, f := range found {
		if reg.Contains(f.Path, host.LocalAlias) {
			knownCount++
		} else {
			newCount++
		}
	}
	fmt.Fprintf(&b, "Would import %d repos (%d new, %d already known):\n", len(found), newCount, knownCount)
	for _, f := range found {
		fmt.Fprintf(&b, "  [%s] %s\n", f.IDE, f.Path)
	}
	return b.String()
}
