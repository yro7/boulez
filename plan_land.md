# Plan : Action « Land instance » — commit + push + merge vers main en un geste

> Réduit la friction du workflow « 3 agents en parallèle → tout ramener sur
> main ». Aujourd'hui chaque instance demande : `p` (commit+push) puis un
> changement de contexte vers `boulez ctl merge` côté shell, le tout ×3. Ce plan
> introduit une action TUI unique qui enchaîne les trois, derrière une garde
> explicite qui lève la protection `main`/`master` **uniquement** pour ce
> syscall top-level.

## Contexte / état de l'art

Ce qui existe déjà :

- **Commit + push = déjà UNE action.** La touche `p` (`keys.KeySubmit`) dans
  `app/app.go` lance `worktree.PushChanges(commitMsg, true)` qui stage, commit
  (`--no-verify`) et pousse la branche, derrière une modale de confirmation.
- **Le merge existe mais n'est pas câblé à la TUI.** `kernel.Kernel.Merge`
  (`kernel/kernel.go:324`) + `session/git.Merger` (`session/git/merge.go`) sont
  implémentés, testés, et défendus (garde `main`/`master` côté Merger +
  garde branche-courante-du-repo-hôte côté kernel). Reachable uniquement via
  `boulez ctl merge` (transport `kernel/transport.go:188`).

Le trou : `Merge` refuse `main`/`master` **par construction**
(`session/git/merge.go:171` `protectedBranches`), doublé côté kernel
(`kernel/kernel.go:330`). C'est volontaire (AGENTS.md : aucune instance ne
touche `main` sans demande explicite). « Land to main » est précisément la
demande explicite : il faut donc un chemin *distinct* de `Merge`, qui incarne
le « l'utilisateur top-level a explicitement demandé à toucher le tronc » —
et non percer la garde de `Merge` (qui doit rester dure pour les workers et
les orchestrators Shape B).

## Décisions verrouillées

1. **Syscall `Land` distinct de `Merge`.** Pas d'option `--force-protected`
   sur `Merge` : cela ouvrirait la porte à un client forgé. `Land` est un
   syscall séparé, dans le wire (`"land"`), qui peut cibler `main`/`master`.
2. **`Land` n'est autorisé que pour un caller top-level.** `IsTopLevel()`
   doit être vrai (caller = `boulez ctl` / TUI, pas un worker ni un
   orchestrator). Un worker/orchestrator qui tente `land` → erreur typée,
   comme `ErrWorkerCannotSpawn`. C'est le miroir exact de la garde de
   récursion : la topologie v1 interdit aux instances de toucher le tronc.
3. **`Land` enchaîne commit-if-dirty + push + merge**, mais la sémantique du
   merge est celle du `Merger` existant : pas de `--abort` silencieux, pas de
   `-X ours/theirs`. Sur conflit, le repo reste en état de merge, le worktree
   est préservé, et `MergeConflict` (avec la liste des fichiers) remonte.
4. **Pas de rebase auto, pas de pull auto.** Si `main` a bougé entre-temps :
   soit le merge fast-forward naturellement (clean), soit conflit →
   résolution humaine (ou agent résolveur, Shape B). Pas de rebase
   automatique silencieux — c'est ce qui rend les merge-bots dangereux.
5. **La cible par défaut est `main`**, configurable (`--target-branch`,
   puis une touche pour changer la cible dans la modale TUI). Le cas
   `--target-branch integration` est aussi valide : `Land` ne refuse que si
   la cible est la branche courante du repo hôte (garde kernel inchangée).
6. **Commit message configurable** mais avec un défaut sensé (réutilise le
   pattern `[boulez] update from '<title>' on <date>`). v1 ne propose
   pas d'éditeur ; v1.1 pourrait ouvrir une modale de saisie.
7. **Une seule instance à la fois en v1.** Pas de « land all ready » dans ce
   plan — c'est l'étape 6 (optionnelle, différée) car le merge séquentiel de
   3 branches qui se chevauchent mérite son propre design (stratégie d'ordre,
   arrêt-au-premier-conflit vs continuer-sur-les-indépendants).

## Architecture

Le `Land` est une orchestration au-dessus de deux briques existantes
(`worktree.PushChanges` + `Merger.Merge`). La nouveauté est mince :

```
TUI (touche L)
   └─ app/app.go : modale de confirmation + appelle session.LandInstance(...)
        └─ session/land.go : LandInstance(inst, targetBranch, commitMsg)
             ├─ worktree.PushChanges(commitMsg, open=false)   [existant]
             └─ kernel.Land(caller top-level, repoPath, targetBranch, [branch])
                  └─ Merger.Merge(...)  [existant, garde main LEVÉE pour ce path]
```

Le kernel expose un nouveau syscall `Land` qui **réutilise le `Merger`** mais
court-circuite la garde `protectedBranches` (uniquement la garde
`main`/`master` du Merger ; la garde « branche courante du repo hôte » reste
active). Pour éviter de percer `defaultMerger.Merge`, on ajoute au `Merger`
une variante ou un flag. Choix retenu : **un flag `AllowTrunk` sur
`MergeResult`-path** via une nouvelle méthode du Merger plutôt qu'un bool sur
l'existante — pour ne pas alourdir l'interface existante. Voir étape 1.

> Note de design (deep module) : on aurait pu ajouter `AllowProtected bool`
> au `Merge(...)` existant. Rejeté : cela rendrait la garde de `Merge`
> contournable par tout appelant, dont les workers Shape B. `Land` est un
> *cas* séparé ; sa surface (un syscall + une méthode de Merger) reste
> petite mais l'isolation du risque est nette.

## Étapes (commits atomiques, chacun vert + testé)

Chaque étape compile et passe `go build ./... && go test ./...`. L'ordre
suit la dépendance (Merger → kernel → wire → TUI).

### Étape 1 — `Merger.MergeTrunk` : merge vers un tronc protégé

**Problème.** `defaultMerger.Merge` refuse `main`/`master` via
`isProtectedBranch`. `Land` doit pouvoir les cibler, sans que `Merge` ne le
puisse.

**Changement.** Ajouter au `Merger` une méthode dédiée :

```go
// MergeTrunk merges sourceBranches into targetBranch, which MAY be a trunk
// (main/master). It exists ONLY for the Land syscall (top-level explicit
// request to land onto the trunk). The regular Merge refuses trunks; this
// path lifts that single guard for the explicit land case. The host-current-
// branch guard is NOT applied here (it lives in the kernel, which knows the
// host repo). On conflict, the repo is left in the merging state.
MergeTrunk(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error)
```

Implémentation : même corps que `Merge` **sans** le `isProtectedBranch`
early-return. Factoriser le corps commun dans une méthode privée
`mergeInto(repoPath, targetBranch, sources, strategy)` que `Merge` et
`MergeTrunk` appellent, `Merge` ajoutant sa garde avant. Évite la
duplication (DRY).

**Tests** (`session/git/merge_test.go`, étendre) :
- `TestMerger_MergeTrunk_AcceptsMain` : `MergeTrunk(repo, "main", ["feat"],
  StrategyDefault)` réussit (clean merge). Le test équivalent sur `Merge`
  échoue toujours (garde non levée).
- `TestMerger_MergeTrunk_ConflictNotAborted` : sur conflit, `MergeTrunk`
  retourne `MergeConflict` + liste de fichiers, et `git status` montre
  encore le merge en cours (pas de `--abort`).
- Le test existant `TestMerger_ProtectedBranchRefused` reste vert (`Merge`
  inchangé dans son comportement).

**Commit :** `feat(git): add Merger.MergeTrunk for explicit trunk landings`

---

### Étape 2 — `kernel.Kernel.Land` : syscall top-level only

**Problème.** `Kernel.Merge` applique la garde kernel (branche courante du
hôte + branches injectées) et délègue au `Merger` qui refuse `main`. Il faut
un syscall `Land` qui (a) n'autorise que les callers top-level, (b) lève la
garde `main`/`master` du Merger, (c) **conserve** la garde branche-courante
du hôte.

**Changement.** `kernel/kernel.go` :

```go
// Land merges a single source branch into a target branch of a repo, with
// the explicit authority to land onto a trunk (main/master). This is the
// "land to main" syscall: it bypasses ONLY the conventional-trunk guard,
// and only for a top-level caller. Workers and orchestrators cannot call it
// (they must use Merge, which refuses trunks). The host-current-branch guard
// still applies: you cannot land into the branch you're standing on.
//
// v1 lands ONE source branch per call (the instance's own branch). On
// conflict, MergeConflict is returned and the repo is left for resolution.
func (k *Kernel) Land(caller CallerContext, repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (git.MergeResult, error)
```

Gardes, dans l'ordre :
1. `if !caller.IsTopLevel()` → `ErrNonTopLevelLand{}` (nouvelle erreur
   typée, `kernel/errors.go`).
2. `if isKernelProtected(k.protectedBranches, targetBranch)` →
   `git.ErrProtectedBranch` (garde hôte **conservée** : on ne land pas dans
   la branche où l'utilisateur se tient).
3. Délègue à `merger.MergeTrunk(repoPath, targetBranch, []string{sourceBranch}, strategy)`.

Pas de plan à mettre à jour (caller top-level n'a pas de plan). Pas
d'enregistrement `RecordMerge` (réservé aux orchestrators).

**Tests** (`kernel/kernel_test.go`, étendre) :
- `TestKernel_Land_TopLevel_AcceptsMain` : caller top-level, cible `main`,
  source `feat`, Merger fake qui réussit → `MergeMerged`, no error.
- `TestKernel_Land_WorkerRefused` : caller worker (`CallerID` non vide,
  `Kind=Worker`) → `ErrNonTopLevelLand`.
- `TestKernel_Land_OrchestratorRefused` : caller orchestrator → idem.
- `TestKernel_Land_HostCurrentBranchStillRefused` : caller top-level mais
  cible = branche injectée via `WithProtectedBranches` → `ErrProtectedBranch`.
- `TestKernel_Land_DelegatesToMergeTrunk` : fake Merger enregistré, vérifie
  que `MergeTrunk` est appelé (pas `Merge`) avec les bons args.

**Commit :** `feat(kernel): add Land syscall (top-level explicit trunk merge)`

---

### Étape 3 — Wire `land` dans le transport + `boulez ctl land`

**Problème.** `Land` doit être reachable. Deux consommateurs : la TUI (étape
4) et le shell (`boulez ctl land`, utile pour scripting / reprise).

**Changement.**
- `kernel/transport.go` : nouveau `case "land"` qui parse `landParams`,
  dérive le caller depuis la session (jamais depuis les params, comme
  `merge`), appelle `k.Land(caller, ...)`. Refuse si la session n'est pas
  top-level (renvoie `NON_TOP_LEVEL_LAND`).
- `cmd_ctl.go` : nouveau sous-mode `boulez ctl land --target-repo <path>
  --target-branch main --source <branch> [--strategy default]`. Réutilise
  `rawCtl`. Documenté dans le long help.
- `kernel/errors.go` : mapper `ErrNonTopLevelLand` → code wire
  `NON_TOP_LEVEL_LAND`.

```go
type landParams struct {
    TargetRepo    string `json:"target_repo"`
    TargetBranch  string `json:"target_branch"`
    Source        string `json:"source"`
    Strategy      int    `json:"strategy,omitempty"`
}
```

**Tests** :
- `kernel/transport_test.go` : `land` top-level réussit et appelle le fake ;
  `land` depuis une session worker → `NON_TOP_LEVEL_LAND`.
- `cmd/cmd_test.go` : round-trip `boulez ctl land` renvoie un JSON pur et
  exécute le Land (fake spawner/merger injectés).

**Commit :** `feat(transport): expose land syscall + boulez ctl land`

---

### Étape 4 — `session.LandInstance` : helper commit+push+land

**Problème.** La TUI ne doit pas réinventer l'enchaînement ; un helper
`session/land.go` le porte, testable isolément.

**Changement.** Nouveau fichier `session/land.go` :

```go
// LandResult is the outcome of LandInstance.
type LandResult struct {
    Pushed  bool             // true if a commit+push happened (worktree was dirty)
    Merge   git.MergeResult // outcome of the kernel Land
}

// LandInstance commits+pushes the instance's worktree (if dirty) then lands
// its branch into targetBranch via the kernel. commitMsg is used only if a
// commit is needed. open=false on push (no browser during a land). On merge
// conflict, the repo is left in merging state and the conflict list is
// returned; the instance is untouched (its worktree is independent of the
// target repo's working tree, so a conflict on main does not corrupt the
// agent's branch).
func LandInstance(inst *Instance, kernelLand LandCaller, targetBranch, commitMsg string) (LandResult, error)
```

Où `LandCaller` est une petite interface (`Land(caller, repo, target, source, strat) (git.MergeResult, error)`) injectée — le kernel la satisfait, les tests
injectent un fake. Garde le helper découplé du package `kernel` (pas
d'import cycle ; `session` ne dépend pas de `kernel`).

Logique :
1. `wt, err := inst.GetGitWorktree()`.
2. `dirty, _ := wt.IsDirty()`. Si dirty : `wt.PushChanges(commitMsg, false)`,
   `Pushed=true`.
3. `repoPath := wt.GetRepoPath()` ; `branch := wt.GetBranchName()`.
4. `merge, err := kernelLand.Land(CallerContext{}, repoPath, targetBranch, branch, git.StrategyDefault)`.
5. Retourner `LandResult{Pushed, merge}` + err.

**Tests** (`session/land_test.go`, nouveau) :
- `TestLandInstance_DirtyCommitsAndPushes` : worktree fake dirty →
  `PushChanges` appelé, `Pushed=true`, `Land` appelé avec la bonne branche.
- `TestLandInstance_CleanSkipsPush` : worktree clean → pas de push,
  `Pushed=false`, `Land` quand même appelé.
- `TestLandInstance_ConflictPropagated` : fake `LandCaller` retourne
  `MergeConflict` → `LandResult.Merge.Status==Conflict`, err nil, worktree
  intact.

**Commit :** `feat(session): add LandInstance helper (commit+push+land)`

---

### Étape 5 — Touche `L` dans la TUI : modale + action

**Problème.** Câbler le helper derrière une touche, avec confirmation qui
montre la cible (pour ne pas land dans `main` par accident).

**Changement.**
- `keys/keys.go` : `KeyLand` + binding `"L"` (help "land → main"). Majuscule
  pour éviter la collision avec `keys.KeyKill` (`D`) — cohérent avec le
  style majuscule-pour-action-destructrice déjà utilisé.
- `ui/menu.go` : ajouter `KeyLand` au `actionGroup` uniquement quand
  l'instance est `Ready` ou `Paused` (pas `Running` — on ne land pas un
  agent en plein travail).
- `app/app.go` : `case keys.KeyLand:` → construit la modale
  `confirmAction` (comme `KeySubmit`) :
  ```
  Land 'fix-auth' into 'main'?
  (commit + push 'fix-auth' then merge into main)
  ```
  L'action appelle `session.LandInstance(selected, m.kernel, "main",
  defaultMsg)`. Sur conflit : `m.handleError` avec un message clair
  (« merge conflict on main, repo left in merging state — resolve and `git
  commit` »). Sur succès : `m.instanceChanged()` (rafraîchit le diff/preview).

Note : `app` a déjà accès au `kernel` ? Vérifier l'injection. Si la TUI
n'a pas encore de référence `*Kernel`, l'étape 5 inclut de la câbler
(passer le kernel à l'app, à côté du spawner). C'est un prérequis pour le
syscall `Land` côté TUI.

**Tests** :
- `app/app_test.go` : presser `L` sur une instance `Ready` ouvre la modale ;
  confirmer appelle `LandInstance` (kernel fake) et ferme la modale.
- Presser `L` sur `Running` : no-op (pas de modale).

**Commit :** `feat(ui): add L key to land an instance into main`

---

### Étape 6 — (Différé, hors v1) « Land all Ready »

Non inclus ici. Le merge séquentiel de N branches qui se chevauchent
mérite son propre design (ordre des sources, arrêt-au-premier-conflit vs
continuer-sur-les-indépendants, surface TUI). À parker dans
`roadmap_and_ideas.md` quand ce plan démarre.

## Non-objectifs (v1)

- Pas d'éditeur de commit message in-TUI (défaut only).
- Pas de choix de stratégie de merge (`--strategy` est reserved ; seul
  `StrategyDefault` est implémenté, comme pour `Merge`).
- Pas de rebase auto, pas de pull auto, pas de PR GitHub auto.
- Pas de résolution auto de conflits (Shape B : spawn d'un worker résolveur).
- Pas de `Land` pour workers/orchestrators (top-level only).

## Risques

- **`PushChanges` actuel fait un `gh repo sync` biscornu** (voir
  `roadmap_and_ideas.md`, entrée ajoutée). `LandInstance` en dépend. Tant
  que ce n'est pas nettoyé, un `Land` peut échouer au push sur des repos
  sans remote GitHub. Le helper propage l'erreur (pas de masquage) ; le
  nettoyage de `PushChanges` est un plan séparé, non bloquant pour la
  mécanique du `Land` lui-même.
- **Conflit sur main laisse le repo hôte en état de merge.** C'est voulu
  (AGENTS.md : pas de `--abort` silencieux). L'utilisateur doit résoudre et
  `git commit`. La modale TUI doit le dire clairement.
- **Course si l'utilisateur land pendant qu'un agent tourne sur la même
  instance.** La garde `Ready`/`Paused` seulement (étape 5) mitigé ; un
  verrou plus strict (refuser `Land` si `Running`) est déjà couvert.
