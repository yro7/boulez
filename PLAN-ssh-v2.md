# Plan v2 : transport SSH (instances sur un host distant)

> Suite de `PLAN-ssh-support.md` (v1 = refactor local-only, terminé). v2
> implémente le transport SSH derrière les seams extraits en v1, plus
> l'AutoYes par-instance et le sélecteur d'host.
>
> Même discipline que v1 : commits atomiques, chacun vert + testé, décisions
> verrouillées. v2 **change le comportement** (c'est la phase feature).

---

## Décisions verrouillées

Reprises de la discussion + affinées par lecture du code v1.

1. **Host primaire, pas produit cartésien** (v1). Flow de création :
   `host → repo-sur-ce-host → branch`.
2. **Auth SSH : binaire `ssh` du système** (v1). `~/.ssh/config`, agent, clés
   OS. Jamais stocker de mot de passe.
3. **AutoYes OFF par défaut sur remote** + toggle par-instance facile + badge
   d'avertissement TUI quand AutoYes ON sur un distant.
4. **`Host` bundle les trois deps** (Executor + FS + PtyFactory) + métadonnées
   (Name, WorktreeDir, AutoYesDefault). Deux implémentations (Local + SSH) =
   seam réel (AGENTS.md : « two means a real one »).
5. **PII : hostnames jamais dans commit messages / branches / noms de session
   tmux** (v1). Le host vit dans `InstanceData.Host` (bookkeeping local).
6. **`PtyFactory` déménage de `session/tmux` vers `host/`.** C'est une
   interface générale « démarrer un process avec PTY », pas spécifique tmux.
   tmux l'importe depuis `host/`. Évite `host → tmux` (cycle) et approfondit
   le module.

## Décisions à valider (mon appel, overridable)

**A. Path generation distante — via expansion `~` du shell distant.**
`SSHHost.WorktreeDir()` retourne le literal `~/.cs2/worktrees` (pas de
résolution `$HOME` réseau). Les commandes `ssh host git -C ~/.cs2/worktrees/...`
sont expansées par le shell distant. `LocalHost.WorktreeDir()` retourne
l'absolu local d'aujourd'hui (`config.GetConfigDir()/worktrees`). Chaque FS
gère sa convention de path (LocalFS = absolu ; SSHFS = `~`-relatif). Évite
un round-trip réseau par host. **Risque :** paths user-typed avec
espaces/quotes → `SSHExecutor` doit shell-quoter (voir #7).

**B. SSH multiplexing — defer.** Chaque `git status` = `ssh host ...` = nouvelle
connexion. Pour v2 on documente ControlMaster dans `~/.ssh/config`
(recommandation), on ne gère pas de master cs2. Le daemon ne poll qu'en
StatusReady/Permission (pas chaque seconde sur chaque instance), donc
supportable. Master managé par cs2 = roadmap (v2.x).

**C. AutoYes global flag — ne touche plus aux remote.** Aujourd'hui
`--auto-yes` force `true` sur toutes les instances chargées (`app.go:168`) et
le daemon force `true` (`daemon.go:34`). v2 : AutoYes devient **vraiment
par-instance** (persisté, respecté). `--auto-yes` ne set le défaut que pour
les **nouvelles instances locales** (préserve l'intention « je veux de
l'auto-yes en local »). Remote : off au créage, toggle TUI requis.
**Changement de comportement local mineur :** les instances locales
*chargées depuis le storage* ne sont plus force-set à true par le daemon —
elles gardent leur valeur persistée. Plus prévisible.

**D. Registre de repos par host — defer.** v2 : `hosts.json` = `[]string`
d'aliases ssh. Local toujours disponible (implicite). Sélection de repo sur
un host distant = **free-text path only** (validé via `ssh host git -C <path>
rev-parse`). Pas de registre per-host en v2 (roadmap, comme les
enrichissements de `repo.Registry` ont été defer en v1).

---

## v2 — Étapes atomiques

### Étape 1 — Package `host` : interface + `LocalHost` + `PtyFactory` déménagé

**Motivation.** Le bundle des trois deps (Executor, FS, PtyFactory) + métadonnées.
LocalHost retourne les defaults d'aujourd'hui. **Zéro behavior change.**

**Fichiers (nouveau package `host/`) :**
- `host/host.go` — interface `Host` :
  ```go
  type Host interface {
      Name() string                          // "local" ou "dev-machine"
      Executor() cmd.Executor
      FS() fs.FS
      PtyFactory() PtyFactory               // (déplacée ici, voir ci-dessous)
      WorktreeDir() (string, error)         // absolu local / ~-relatif distant
      AutoYesDefault() bool                 // local: cfg.AutoYes ; ssh: false
  }
  ```
- `host/pty.go` — interface `PtyFactory` **déplacée** depuis
  `session/tmux/pty.go` (renommée, tmux importe désormais `host.PtyFactory`).
  `LocalPtyFactory` (was `MakePtyFactory`) déménage ici. Implémentation
  inchangée (creack/pty).
- `host/local.go` — `LocalHost` implémente `Host` : Executor=`cmd.Exec`,
  FS=`fs.LocalFS`, PtyFactory=`LocalPtyFactory`, WorktreeDir=`config.GetConfigDir()/worktrees`,
  AutoYesDefault=`cfg.AutoYes`.

**Fichiers (rewiring) :**
- `session/tmux/pty.go` — supprimer l'interface `PtyFactory` + `MakePtyFactory`
  + `Pty` (déplacés vers `host/`). tmux garde ses usages via `host.PtyFactory`.
- `session/tmux/tmux.go` — `ptyFactory` field devient `host.PtyFactory` ;
  `MakePtyFactory()` → `host.LocalPtyFactory()` (ou `host.Local`).
- `session/tmux/*_test.go` — `MockPtyFactory` référence `host.PtyFactory`.
- `session/instance.go` — `Instance` gagne un champ `host host.Host`,
  defaulté à `host.Local{}` dans `NewInstance`/`FromInstanceData`. `Start`
  passe `host.Executor()`, `host.FS()` aux constructeurs `GitWorktree*WithDeps`,
  et `host.PtyFactory()`, `host.Executor()` à `NewTmuxSessionWithDeps`.

**Tests :** existants verts (LocalHost = behavior d'aujourd'hui). Ajouter un
test que `LocalHost.WorktreeDir()` retourne le même path que l'ancien
`getWorktreeDirectory()` (garantie de non-régression).

**Commit :** `refactor(host): introduce Host interface with LocalHost, move PtyFactory`

---

### Étape 2 — `SSHHost` : Executor + FS + PtyFactory over ssh

**Motivation.** Première implémentation distante. Tout passe par `ssh host ...`.

**Fichiers :**
- `host/ssh.go` — `SSHHost{ alias }` implémente `Host` :
  - `Executor()` → `sshExecutor{ alias }` : wrap chaque `*exec.Cmd` en
    `exec.Command("ssh", alias, origArgs...)`. **Shell-quote les args** (safety #7).
  - `FS()` → `sshFS{ alias }` : Stat/RemoveAll/MkdirAll/ReadDir via
    `ssh host sh -c '...'` (paths `~`-relatifs, expansés par le shell distant).
  - `PtyFactory()` → `sshPtyFactory{ alias }` : `Start` lance
    `exec.Command("ssh", "-t", alias, ...)` via `pty.Start` (creack/pty).
  - `WorktreeDir()` → retourne le literal `~/.cs2/worktrees` (décision A).
  - `AutoYesDefault()` → `false` (décision 3).
- `host/ssh_test.go` — tests avec **faux Executor** qui assert que les
  commandes sont wrappées en `ssh <alias> <orig...>` et que les args sont
  shell-quotés (path avec espace). Pas de vrai ssh en CI.

**Tests :** faux-executor assert du wrapping `ssh`. Test du shell-quoting
(path `"/home/me/my repo"` → arg quoté).

**Commit :** `feat(host): add SSHHost (Executor/FS/PtyFactory over ssh)`

---

### Étape 3 — Path generation via `Host.WorktreeDir()`

**Motivation.** `getWorktreeDirectory()`/`resolveWorktreePaths` retournent un
path local today ; pour remote, doivent retourner le path du distant.

**Fichiers :**
- `session/git/worktree.go` — `resolveWorktreePaths` prend un `host.Host` (ou
  le `WorktreeDir`) au lieu d'appeler `getWorktreeDirectory()`. Les
  constructeurs `GitWorktree*WithDeps` reçoivent le Host (ou le dir résolu).
- Supprimer `getWorktreeDirectory()` (ou le garder pour LocalHost
  uniquement). `Instance.Start` passe `i.host` aux constructeurs GitWorktree.

**Tests :** test que `LocalHost` produit le même path qu'avant (non-régression).
Test (futur) que `SSHHost` produit `~/.cs2/worktrees/...`.

**Commit :** `refactor(git): resolve worktree path via Host.WorktreeDir`

---

### Étape 4 — Registre d'hosts + sélecteur + `InstanceData.Host`

**Motivation.** Persistence du host sur l'instance + UI de sélection.

**Fichiers :**
- `host/registry.go` — `Registry` (miroir `repo.Registry`), `~/.cs2/hosts.json`,
  `[]string` d'aliases (décision D). `List/Add/Remove/Contains`.
- `host/registry_test.go` — round-trip, dedup.
- `ui/overlay/hostSelector.go` — copie de `RepoSelector` (registre + free-text
  alias). Local toujours en tête.
- `session/storage.go` — `InstanceData.Host string` (alias ou `"local"`).
- `session/instance.go` — `FromInstanceData` lookup le Host via un
  `host.Registry` (ou un resolver `host.Lookup(alias)`) et l'injecte. Si alias
  inconnu → erreur claire (host retiré du registre).
- `app/app.go` — flow de création : `host → repo-sur-ce-host → branch`.
  `RepoSelector` filtre/re-valide le path via `host.Executor()` (pour remote,
  free-text only en v2).

**Tests :** round-trip InstanceData.Host. Restauration avec alias inconnu → erreur.

**Commit :** `feat(host): add host registry, selector, and InstanceData.Host`

---

### Étape 5 — AutoYes par-instance + policy + toggle TUI

**Motivation.** Décision 3 + C. AutoYes vraiment par-instance, off par défaut
sur remote.

**Fichiers :**
- `daemon/daemon.go` — **supprimer** `instance.AutoYes = true` (l.34).
  Le daemon respecte `instance.AutoYes` (persisté).
- `app/app.go` — `:168` et `:368` : `if autoYes && instance.Host().AutoYesDefault()`
  (ne force plus sur remote). Nouvelles instances : `instance.AutoYes = host.AutoYesDefault()`.
- `keys/keys.go` + `app/app.go` — touche `a` (toggle AutoYes par-instance).
  Si flip ON sur un host distant → badge/confirmation d'avertissement.
- `ui/list.go` — badge AutoYes ON + avertissement si distant.

**Tests :** daemon ne force plus true. Nouvelle instance remote → AutoYes=false.
Toggle flip la valeur persistée.

**Commit :** `feat(autoyes): per-instance AutoYes with remote-off default and TUI toggle`

---

### Étape 6 — Attach distant + intégration

**Motivation.** `SSHHost.PtyFactory()` lance `ssh -t host tmux attach -t
<session>` via creack/pty. Le seam existe déjà ; c'est de l'intégration.

**Fichiers :**
- `host/ssh.go` — `sshPtyFactory.Start` pour `tmux attach` (déjà couvert par
  l'Étape 2 pour le démarrage ; vérifier le path attach).
- Test d'intégration manuel (pas en CI — nécessite un vrai host). Documenter
  dans `README.md` : préconditions (agent + tmux installés sur le distant),
  recommandation ControlMaster (décision B), port-forwarding à l'utilisateur.

**Commit :** `feat(host): enable remote tmux attach via SSHHost.PtyFactory`

---

### Étape 7 — Sécurité / PII / shell-quoting

**Motivation.** Verrouiller #5 et #7. Déjà partiellement fait en Étape 2
(shell-quoting du SSHExecutor), ici on audit et on documente.

**Fichiers :**
- `host/ssh.go` — `shellQuote` helper + tests (path avec espace, quote,
  backtick ; assert pas d'injection).
- Audit PII : hostnames n'apparaissent dans aucun commit message (`instance.go`
  Pause/Resume), nom de branche (`sanitizeBranchName` déjà safe), nom de
  session tmux (`toClaudeSquadTmuxName` — vérifier).
- `roadmap_and_ideas.md` — persister ControlMaster (dette v2.x) et le
  registre per-host (dette v2.x).

**Commit :** `feat(host): harden SSH shell-quoting and PII audit`

---

### Étape 8 — `CleanupWorktrees` host+repo-aware

**Motivation.** Clôt la dette #1 (bug latent multi-repo + cleanup distant).

**Fichiers :**
- `session/git/worktree_ops.go` — `CleanupWorktrees` prend un `host.Host` (ou
  est retiré au profit d'un cleanup per-instance déjà routé par `g.cmdExec`/`g.fs`).
  La forme précise émerge des besoins v2 observés.

**Tests :** cleanup multi-repo ne corrompt pas cwd. Cleanup distant route par
le Host.

**Commit :** `fix(git): make CleanupWorktrees host+repo-aware (clears debt #1)`

---

## Critères de succès v2

1. `go build ./...` + `go test ./...` verts après chaque commit.
2. Une instance peut cibler un host distant (`ssh dev-machine`) : worktree,
   tmux, agent tournent sur le distant ; TUI local supervise.
3. Scenario benchmark : 3 instances `(A, L40S)`, `(A, GTX)`, `(A, H100)` +
   `(B, local)` coexistent dans la liste plate.
4. AutoYes OFF par défaut sur toute instance remote ; toggle `a` l'active
   avec avertissement.
5. Aucun hostname dans commit messages / branches / noms de session tmux.
6. `program.Adapter` toujours non touché.
7. Dette #1 (`CleanupWorktrees`) résolue ; dettes v2.x (ControlMaster,
   registre per-host) persistées dans `roadmap_and_ideas.md`.

## Hors scope v2

- Master SSH managé par cs2 (ControlMaster) — v2.x.
- Registre de repos par host — v2.x.
- Port-forwarding automatique des dev servers distants — v2.x.
- Support non-GitHub (dette #2) — feature séparée.
- Design TUI final (rendu badges, avertissements) — « design en dernier ».
