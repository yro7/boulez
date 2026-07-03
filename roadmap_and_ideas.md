# Roadmap & Ideas — cs2

> Idées repoussées (pas au scope actuel), décisions produits en attente,
> et choses implémentées. Ce fichier est un parking, pas un plan d'action.
> Rien ici n'est engagé.

---

## Implémenté

### Import one-shot des repos depuis les IDE (VS Code et forks)

**Commande.** `cs2 repo-import` (avec `--dry-run` et `--ide <name>`).
Code isolé dans le package `ideimport/`.

**Contexte / idée reçue.** VS Code ne maintient PAS une liste de repos git.
Il a une liste de *dossiers* récemment ouverts (« Open Recent »), stockée
dans `~/Library/Application Support/<IDE>/User/globalStorage/storage.json`
(macOS), format interne non stable. Donc « récupérer les repos de VS Code » =
en pratique parser ce `storage.json`, puis filtrer pour ne garder que les
repos git (`git.IsGitRepo`).

**Décision produit.** Import *one-shot, manuel* — jamais de couplage permanent
(lecture à chaque démarrage interdite). Raisons : format non documenté et
instable, dépendance à ce que l'utilisateur utilise un IDE VS Code-fork —
tout cela contredirait le principe « standalone, agent-agnostic » du fork.
Si le format IDE casse, l'import échoue ou se vide, mais cs2 tourne toujours.

**Implémentation.**

- Scan tous les IDEs VS Code-forks installés par défaut, ou un seul via
  `--ide` : `vscode`, `cursor`, `windsurf`, `antigravity`, `vscodium`,
  `pearai`, `void`, `trae`.
- Parse robuste : **walk récursif du JSON**, ne dépend d'aucun nom de clé —
  survit aux variations de format entre IDEs et versions.
- Ne collecte que les URLs `file://` (les paths absolus nus sont ignorés pour
  éviter de récolter machine IDs / tokens — AGENTS.md : pas de fuite sensible).
- `--dry-run` lit le registre en lecture seule pour distinguer nouveau vs déjà
  connu, sans rien écrire.
- Écriture idempotente via `Registry.Add` (dédup + résolution absolue).

---

## Idées repoussées

*(Aucune idée repoussée pour l'instant — l'import IDE, anciennement ici, a
été implémenté, voir ci-dessus.)*

---

## Richesse du registre de repos

**Contexte.** Le registre cs2 (décision actuelle) stocke une liste de repos
connus. Question de forme : liste de paths nus, ou structure plus riche
(alias, repo par défaut, tri par récence).

**Décision actuelle.** Format **minimal : liste de paths**
(`repos: ["~/projA", ...]`). Un repo = une string, le titre affiché dans le
sélecteur = basename du path. KISS, pas d'abstraction spéculative.

**Idées repoussées (à réévaluer si besoin).**

1. **Alias par repo** — `repos: [{path, alias}]`. Permet de nommer un repo
   (« frontend » au lieu de `~/work/big-monorepo/frontend`). Devient
   pertinent si : deux repos ont le même basename, ou des paths longs
   illisibles dans le sélecteur. Migration triviale depuis `[]string` le
   jour où le besoin arrive (alias vide = path).

2. **Repo par défaut** — un repo coché en premier dans le sélecteur,
   utilisé si l'utilisateur ne choisit pas. Devient pertinent si un repo
   est nettement plus utilisé que les autres (évitateur de friction). À
   réévaluer une fois le multi-repo en usage réel, pour voir si un repo
   émerge comme dominant.

3. **Tri par récence** — les repos les plus récemment utilisés remontent
   en haut du sélecteur. Pertinent si le registre grossit (>10 repos) et
   que le même petit set revient souvent. À réévaluer selon le volume
   réel observé.

**Pourquoi ce n'est pas implémenté maintenant.** Aucun de ces besoins n'est
confirmé par l'usage. Les ajouter préventivement imposerait une UI de
configuration (comment set l'alias ? comment marquer un défaut ?) qu'on ne
veut pas coder tant que « design TUI en dernier » tient. Le format minimal
se migrera trivialement le moment venu.

---

## Dette technique (observée pendant le refactor SSH v1)

*(Choses vues mais non corrigées pendant le refactor v1, pour rester
atomique. v2 part de cette liste explicite — rien ne reste implicite.
Voir `PLAN-ssh-support.md`, décision 8.)*

1. **`CleanupWorktrees` (`session/git/worktree_ops.go:165`)** — ~~lance
   `git worktree list` **sans `-C`** (opère sur cwd) et `git branch -D`
   sans contexte repo. Bug latent multi-repo, pré-existant.~~ **Résolu en
   v2 (étape 8).** `CleanupWorktreesWithDeps(cmd.Executor, fs.FS, worktreeDir)`
   route chaque worktree via `git -C <repo-root> ...` (repo-aware, indépendant
   du cwd) et passe par l'Executor/FS injectés. Le wrapper sans args
   `CleanupWorktrees()` defaulte au local (zéro ripple côté `main.go`). Tests :
   multi-repo (deux repos, cwd neutre), routing via fakes, fallback orphelin.

2. **Couplage `gh` (GitHub CLI) dans `PushChanges` / `OpenBranchURL` /
   `checkGHCLI` (`session/git/worktree_git.go`).** Rend cs2 inopérant sur
   GitLab / local-host. Vrai problème, mais c'est un **autre feature**
   ("support non-GitHub"), pas du SSH. L'ouvrir maintenant = scope creep.
   Un plan séparé le traitera.

3. **`worktree_branch.go` = 1 fonction (`combineErrors`).** Module trop
   peu profound. À fusionner dans `worktree_ops.go` ou `worktree_git.go`
   si on touche ces fichiers, sinon reporter.

### Dette technique (observée pendant le transport SSH v2)

*(Choses vues pendant v2 mais non corrigées, pour rester atomique. Reprend
la décision 8 : la dette est persistée au fur et à mesure.)*

1. **Master SSH managé par cs2 (décision B du plan, defer).** Chaque opération
   `git status` / `tmux has-session` ouvre une nouvelle connexion SSH. v2
   documente ControlMaster dans `~/.ssh/config` (recommandation utilisateur) ;
   cs2 ne gère pas de master lui-même. À réévaluer si le polling distant
   multi-instances devient trop lent. Forme possible : un master partagé par
   host alias, démarré/tué par cs2.

2. **Registre de repos per-host (décision D du plan, defer).** v2 ne maintient
   qu'un registre d'aliases SSH (`~/.cs2/hosts.json`), pas de mapping
   host → repos connus. La sélection d'un repo sur un host distant est du
   free-text path uniquement (validé via `ssh host git -C <path> rev-parse`).
   Un registre per-host deviendrait pertinent si l'on réutilise souvent les
   mêmes repos distants — migration triviale quand le besoin émerge.

3. **`sshExecutor` ignore `cmd.Dir` / `cmd.Env`.** `SSHHost.Executor()` wrap
   `exec.Command(sshBin, alias, joinShellQuoted(c.Args)...)` — il reconstruit
   la commande et droppe silencieusement `c.Dir` et `c.Env`. Aucun appelant
   git n'est affecté (tous utilisent `git -C <path>`, pas `.Dir`). Les seuls
   appelants utilisant `.Dir` sont les commandes `gh` (`PushChanges`,
   `OpenBranchURL`) — qui sont déjà la dette #2 (couplage gh). Donc ce
   comportement est subsumé par le découplage gh : quand `gh` sera routé
   correctement (soit abstrait, soit via `git -C`-équivalent), le `.Dir`
   deviendra moot. Noté pour ne pas l'oublier.

4. **Audit PII v2 effectué (décision 5, vérifiée).** L'alias SSH ne flue que
   vers `InstanceData.Host` (bookkeeping local) + `host.Lookup` (résolution
   au restore) + `pendingHost` (flow de création). Il n'apparaît **jamais**
   dans : les commit messages (`pausedCommitMessage(title, t)` — signature
   sans host), les noms de branche (`cs2/` + `sanitizeBranchName(title)`),
   ni les noms de session tmux (`toClaudeSquadTmuxName(title)`). Invariant
   structurel, piné par `TestInstance_PII_HostAliasNotInArtifacts`. Pas une
   dette — un audit clôturé, consigné ici pour traçabilité.
