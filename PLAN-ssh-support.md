# Plan : support SSH (instances sur un host distant)

> Objectif : permettre à une instance boulez de tourner sur un **host distant**
> (ex. `dev-machine`, `gpu-box`) tout en restant supervisée depuis le TUI
> local. Le TUI devient un dashboard unifié d'instances réparties sur
> plusieurs machines — ex. `(A, local)`, `(A, L40S)`, `(A, H100)`, `(B, H100)`.
>
> Ce plan est **scindé en deux phases** :
>
> - **v1 (ce document, étapes 1–4)** : refactor **local-only, zéro changement
>   de comportement**. On extrait les seams (Executor pour git, interface FS)
>   que le SSH branchera ensuite, **et on nettoie la couche git au passage**
>   (harmonisation des patterns, type `Repo`). À l'issue de v1,
>   `go build ./...` et `go test ./...` sont verts et rien n'a changé pour
>   l'utilisateur.
> - **v2 (aperçu en fin de document)** : implémentation du transport SSH
>   derrière les seams de v1, plus l'AutoYes par-instance et le sélecteur
>   d'host.
>
> Inspiré de `PLAN-multi-repo.md` : commits atomiques, chacun vert + testé,
> décisions verrouillées issues de la discussion.

---

## Contexte technique (pourquoi v1 est un refactor, pas une feature)

L'environnement d'une instance boulez est **tout-ou-rien par machine** : le
repo, le worktree, le serveur tmux et le process de l'agent doivent être sur
le **même host** (l'agent édite des fichiers dans son CWD = le worktree).
Pas de « git distant + agent local ». Donc « remote » = déplacer
{l'environnement git + tmux + agent} vers le distant, et le TUI local ne fait
que superviser (lancer, capturer le pane, attach).

L'architecture modulaire existante tient le choc, mais avec **une asymétrie
majeure** mise en évidence par la lecture du code :

| Couplage         | État actuel                                                                                       |
| ---------------- | ------------------------------------------------------------------------------------------------- |
| **tmux**         | **Déjà discipliné.** `cmd.Executor` et `PtyFactory` sont **injectés** (`session/tmux/tmux.go:81`, `NewTmuxSessionWithDeps`). ~90% prêt pour SSH. |
| **git**          | **Pas du tout abstrait, et sale.** `runGitCommand` (`worktree_git.go:58`) construit `exec.Command("git", ...)` directement. 4 patterns d'invocation différents coexistent (voir ci-dessous). |
| **FS (os.\*)**   | Appels `os.Stat`/`os.RemoveAll`/`os.MkdirAll`/`os.ReadDir` directement sur `worktreePath` dans `session/git/` et `instance.go` (Pause). Assument silencieusement un FS local. |
| **program.Adapter** | **Ne bouge pas.** `Detect(content)` est du parsing de texte de pane, host-agnostic par construction. Belle validation a posteriori de la modularité. |

### La couche git a 4 patterns d'invocation (violation DRY)

Audit de `session/git/` — quatre façons d'invoquer git coexistent :

| # | Pattern | Où | Problème |
|---|---------|----|---------| 
| 1 | `g.runGitCommand(path, args)` avec `-C` | `worktree_git.go:58` | Le bon. Mais pas injecté (construit `exec.Command` direct). |
| 2 | `exec.Command("git", "-C", repoPath, ...)` direct, package-level | `FetchBranches`, `SearchBranches` (`worktree_git.go:13,27`), `findGitRepoRoot`, `IsGitRepo` (`util.go:53,58`) | Échappe à toute injection. Aucune cohésion au worktree. |
| 3 | `exec.Command("gh", ...).Dir = path` | `PushChanges`, `OpenBranchURL` | Pattern `Dir` au lieu de `-C`. Couplage `gh` (voir dette #2). |
| 4 | `exec.Command("git", "worktree", "list")` **sans `-C`** | `CleanupWorktrees` (`worktree_ops.go:171`) | Bug latent multi-repo : opère sur cwd. |

Et `worktree_git.go` mélange **3 responsabilités** sans rapport :
ops de worktree (`IsDirty`, `CommitChanges`…), recherche de branches
repo-level (`FetchBranches`, `SearchBranches`), et intégration GitHub CLI
(`checkGHCLI`, `PushChanges`, `OpenBranchURL`). Plus `worktree_branch.go` =
un fichier d'**une seule fonction** (`combineErrors`) — module trop peu
profound.

v1 harmonise les patterns 1–3 vers l'Executor (le #4 est hors scope, voir
dette #1). C'est opportuniste : puisqu'on route chaque appel par l'Executor
de toute façon, le coût marginal d'harmoniser *en même temps* est ~nul, et
ne pas le faire figerait la saleté sous l'abstraction.

### Découverte complémentaire : AutoYes est global aujourd'hui

Le daemon force `instance.AutoYes = true` sur **toutes** les instances
(`daemon/daemon.go:34`), et `app.go:168` pareil via un flag global. Il n'y a
pas d'AutoYes par-instance respecté. La décision « off par défaut sur remote »
impliquera de rendre AutoYes *vraiment* par-instance en v2. **Hors scope v1**
(local only, comportement inchangé).

---

## Décisions verrouillées (issues de la discussion)

1. **Host primaire, pas produit cartésien.** Le flow de création est
   `host → repo-sur-ce-host → branch`. Un path n'a de sens que relatif à un
   host ; boulez ne maintient pas de mapping spéculatif « même repo logique
   entre hosts ». Le « produit cartésien » apparaît dans la **liste des
   instances**, pas dans la **sélection**. Chaque host a sa propre liste de
   repos connus (paths valides sur CE host).
2. **Auth SSH : s'appuyer sur le système, jamais réimplémenter.** Toujours
   invoquer le binaire `ssh` (config `~/.ssh/config`, agent, clés OS). Ne
   jamais stocker de mot de passe. Approche VS Code.
3. **AutoYes OFF par défaut sur les hosts non locaux** + capacité de le
   mettre sur ON très facilement (toggle par-instance, pas de gate lourd,
   mais badge d'avertissement visible dans le TUI quand AutoYes est ON sur un
   host distant). Implique de rendre AutoYes *vraiment* par-instance en v2.
4. **v1 = refactor local-only, zéro behavior change, + nettoyage opportuniste de la couche git.** On extrait les seams
   (Executor pour git, interface FS) et on harmonise les 4 patterns
   d'invocation git vers un seul. v2 branche le transport SSH derrière.
5. **Type `Repo` introduit en v1.** Pas spéculatif : 4 fonctions partagent
   déjà le même besoin (un `path` + bientôt un `Executor`) — cohésion réelle,
   pas une abstraction à une implémentation. C'est aussi **nécessaire pour
   v2** : le branch-picker à la création scanne les branches du repo *distant*,
   donc ces fonctions DOIVENT devenir SSH-aware. Mieux vaut lander ça en
   local-only maintenant qu'en v2 en pleine tempête SSH.
6. **Pas de package `Host` spéculatif en v1.** (AGENTS.md : « one adapter
   means a hypothetical seam; two means a real one ».) En v1, `Instance`
   tient un `cmd.Executor` + un `FS` séparément. Le bundle `Host` (qui
   regroupe Executor + FS + PtyFactory) est la **première étape de v2**,
   quand `SSHHost` (2e implémentation) arrive pour de vrai.
7. **Discipline PII étendue aux hostnames.** Le commit `8103394` a statiqué
   le préfixe `boulez/` pour empêcher la fuite PII dans les noms de branches.
   Les noms de hosts (`dev-machine`, `L40S`, `H100`) sont la même classe de
   risque : **jamais dans les commit messages / noms de branche / noms de
   session tmux**. Le host vit uniquement dans `InstanceData.Host`
   (bookkeeping local du TUI). L'utilisateur choisit le titre d'instance
   librement.
8. **Dette technique persistée.** Tout besoin/dette observé pendant le
   refactor v1 (choses qu'on ne fixe pas maintenant pour rester atomique)
   est **écrit** dans une section dédiée de `roadmap_and_ideas.md` :
   « Dette technique (observée pendant le refactor SSH v1) ». Rien ne reste
   implicite — v2 partira d'une liste explicite. (Voir « Persistance de la
   dette » ci-dessous.)

---

## v1 — Refactor local-only (étapes atomiques)

Principe : chaque commit laisse `go build ./...` et `go test ./...` verts.
Aucun changement visible pour l'utilisateur. Les seams deviennent
**testables sans SSH** via de fausses implémentations — c'est la preuve que
v2 sera un ajout pur.

### Étape 1 — Étendre `cmd.Executor` avec `CombinedOutput`

**Motivation.** `runGitCommand` utilise `cmd.CombinedOutput()` (pour capturer
stderr dans le message d'erreur). L'interface `cmd.Executor` actuelle n'a que
`Run` et `Output`. Pour router git via l'Executor sans perte de comportement,
il faut `CombinedOutput` sur l'interface. Additif, tmux non affecté.

**Fichiers :**
- `cmd/cmd.go` — ajouter `CombinedOutput(cmd *exec.Cmd) ([]byte, error)` à
  l'interface `Executor` + impl sur `Exec` (délègue à `cmd.CombinedOutput()`).
- `cmd/cmd_test/testutils.go` — tout mock Executor existant doit implémenter
  la nouvelle méthode (trivial).

**Tests :**
- `cmd/` — test que `Exec.CombinedOutput` retourne bien stdout+stderr.

**Commit :** `refactor(cmd): add CombinedOutput to Executor interface`

---

### Étape 2 — Introduire le type `Repo` (ops repo-level + Executor injecté)

**Motivation.** Les 4 fonctions package-level `FetchBranches`, `SearchBranches`,
`findGitRepoRoot`, `IsGitRepo` partagent le même besoin (un `path` + un
`Executor`) mais n'ont aucune cohésion au worktree. On les regroupe dans un
type `Repo` qui porte l'Executor. C'est le split SRP honnête : `Repo` = ops
sur un repo git existant (branches, fetch, racine), `GitWorktree` = ops sur
un worktree boulez spécifique (diff, commit, dirty). Le branch-picker de
`PLAN-multi-repo.md` (qui scanne les branches *avant* qu'un worktree existe)
devient un client naturel de `Repo` — cohérence avec l'existant.

**Fichiers (nouveau fichier) :**
- `session/git/repo.go` — type `Repo` :
  ```go
  // Repo wraps a repository path with an injectable command executor. It
  // owns repo-level operations (branches, fetch, root resolution) that have
  // no dependency on a boulez worktree. Adding SSH = swap the Executor; Repo
  // itself is transport-agnostic.
  type Repo struct {
      path    string
      cmdExec cmd.Executor
  }
  func NewRepo(path string) *Repo                       // default local Executor
  func NewRepoWithDeps(path string, exec cmd.Executor) *Repo
  func (r *Repo) Path() string
  func (r *Repo) FetchBranches()                         // was package-level
  func (r *Repo) SearchBranches(filter string) ([]string, error)
  func (r *Repo) Root() (string, error)                  // was findGitRepoRoot
  func (r *Repo) BranchExists(name string) (bool, error) // extracted from worktree_ops inline show-ref
  ```
  Toutes les méthodes routent par `r.cmdExec` et utilisent `-C r.path`.
  Harmonisation : pattern 2 → pattern 1.

**Fichiers (migration) :**
- `session/git/util.go` — supprimer `findGitRepoRoot` (devient `Repo.Root`)
  et `IsGitRepo` (devient `Repo`-based ou garde un helper qui construit un
  `Repo` temporaire). Conserver `sanitizeBranchName`, `checkGHCLI`.
- `session/git/worktree_git.go` — supprimer `FetchBranches`, `SearchBranches`
  (déplacées vers `Repo`).
- `session/git/worktree.go` — `resolveWorktreePaths` utilise `NewRepo(absPath).Root()`
  au lieu de `findGitRepoRoot(absPath)`.
- Appelants (`app/app.go` pour le branch-picker) — migrer de
  `FetchBranches(os.Getwd())` / `SearchBranches(repoPath, ...)` vers
  `repo.FetchBranches()` / `repo.SearchBranches(...)`. Construction d'un `Repo`
  à partir du `repoPath` déjà disponible (cf. Étape 5/6 de `PLAN-multi-repo.md`).

**Note — `GitWorktree` ne délègue pas à `Repo` en v1.** `GitWorktree` tient
déjà son `repoPath` + (à l'Étape 3) son `Executor`. Ses ops repo-level
(`show-ref refs/heads/<branch>` dans `worktree_ops.go`) restent sur
`GitWorktree` — triviales, pas de couplage spéculatif. Si v2 a besoin, on
verra. Évite d'emboîter deux types sans raison confirmée.

**Tests :**
- `session/git/` — tests existants verts (migration mécanique).
- Ajouter un test avec un **faux Executor** sur `Repo` qui prouve que
  `SearchBranches` route par `r.cmdExec` avec les bons args `(git, -C, path,
  branch, -a, ...)`. Preuve du seam repo-level avant SSH.

**Commit :** `refactor(git): introduce Repo type for repo-level ops with injectable Executor`

---

### Étape 3 — Injecter l'Executor dans `GitWorktree` (miroir tmux `WithDeps`)

**Motivation.** Rendre toutes les opérations du worktree routables via un
Executor injecté, comme tmux le fait déjà. Suit exactement la convention
établie par `NewTmuxSession` / `NewTmuxSessionWithDeps`. Harmonise les
patterns 1 et 3 au passage.

**Fichiers :**
- `session/git/worktree_git.go` — `runGitCommand` construit le `*exec.Cmd`
  puis appelle `g.cmdExec.CombinedOutput(cmd)` au lieu de
  `cmd.CombinedOutput()` directement. Harmonise les appels directs
  `exec.Command` éparpillés par `g.cmdExec` : `fetch --prune` (l.17, si
  encore là), `branch -a` (l.25, idem), `push` dans `PushChanges` (l.101,
  qui utilise `Run` → `g.cmdExec.Run`).
- `session/git/worktree.go` — champ `cmdExec cmd.Executor` sur `GitWorktree`.
  Constructeurs :
  - `NewGitWorktree`, `NewGitWorktreeFromBranch`, `NewGitWorktreeFromStorage`
    gardent leur signature publique, initialisent `cmdExec` à
    `cmd.MakeExecutor()` en interne (**zéro ripple côté appelants**).
  - Ajouter `NewGitWorktreeWithDeps(...)` (et variantes) pour tests/v2,
    prenant un `cmd.Executor` explicite. Miroir exact de
    `NewTmuxSessionWithDeps`.

**Note — `gh` et `PushChanges`/`OpenBranchURL` :** routés par `g.cmdExec`
(pattern 3 → 1) mais **non abstraits**. Le couplage `gh` est une dette
séparée (voir « Persistance de la dette »). On ne fixe pas le support
non-GitHub maintenant — c'est un autre feature.

**Note — `CleanupWorktrees` laissé tel quel.** Bug latent multi-repo
(signalé ci-dessous, dette #1). Hors scope v1. On ne le route pas via
Executor maintenant (ce serait le réparer à moitié) ; v2 s'en charge quand
les vrais besoins auront émergé.

**Tests :**
- `session/git/` — tests existants restent verts (constructeurs par défaut
  → comportement inchangé).
- Ajouter un test avec un **faux Executor** (enregistre les commandes
  reçues) qui prouve que `runGitCommand` route bien par `g.cmdExec` et que
  les args sont `(git, -C, path, ...args)`. C'est la preuve du seam avant SSH.

**Commit :** `refactor(git): inject Executor into GitWorktree (mirror tmux WithDeps)`

---

### Étape 4 — Introduire l'interface `FS` + `LocalFS`, router les `os.*`

**Motivation.** Les appels `os.Stat`/`os.RemoveAll`/`os.MkdirAll`/`os.ReadDir`
sur `worktreePath` assument un FS local. Pour une instance distante, ces
paths sont distants et un `os.Stat` local est un **bug silencieux**, pas une
erreur réseau. On abstrait le FS derrière une interface injectable.

**Fichiers (nouveau package) :**
- `session/fs/fs.go` — package `fs`. Interface :
  ```go
  type FS interface {
      Stat(name string) (os.FileInfo, error)
      RemoveAll(path string) error
      MkdirAll(path string, perm os.FileMode) error
      ReadDir(name string) ([]os.DirEntry, error)
  }
  ```
- `session/fs/local.go` — `LocalFS` implémentant `FS` par délégation à
  `os.Stat` / `os.RemoveAll` / `os.MkdirAll` / `os.ReadDir`. Comportement
  strictement identique à aujourd'hui.
- `session/fs/fs_test.go` — round-trip : `LocalFS` se comporte comme `os.*`
  (créer, stat, lister, supprimer).

**Fichiers (routage) :**
- `session/git/worktree.go` — champ `fs fs.FS` sur `GitWorktree`. Constructeurs
  par défaut l'initialisent à `fs.LocalFS{}` (zéro ripple). WithDeps le prend
  explicite.
- `session/git/worktree_ops.go` — remplacer `os.MkdirAll`, `os.RemoveAll`,
  `os.Stat` par `g.fs.*`. (Pas `CleanupWorktrees` — hors scope.)
- `session/git/worktree_git.go` — `IsValidWorktree` route ses `os.Stat` par
  `g.fs.Stat`.
- `session/instance.go` — **déplacer** les `os.Stat`/`os.RemoveAll` du
  `Pause()` (`instance.go`) dans des **méthodes sur `GitWorktree`** (ex.
  `WorktreeDirExists() bool`, `RemoveWorktreeDir() error`). Rationale SRP :
  l'`Instance` ne doit pas connaître le FS ; le `GitWorktree` possède ses
  fichiers. Ainsi `Instance` n'a pas besoin de tenir un `FS` du tout — seul
  `GitWorktree` en a un.

**Tests :**
- `session/git/` — tests existants verts.
- Ajouter un test avec un **faux FS** (enregistre les paths accédés) qui
  prouve que `Pause` d'une instance orphaned passe par `g.fs.Stat` /
  `g.fs.RemoveAll` avec le bon `worktreePath`. Preuve du seam.

**Commit :** `refactor(fs): introduce FS interface with LocalFS, route file ops through it`

---

## Persistance de la dette (décision 8)

Pendant le refactor v1, toute dette/need observé et **non fixé** (pour rester
atomique ou parce qu'il émerge d'un cas d'usage pas encore confirmé) est
**écrit** dans une nouvelle section de `roadmap_and_ideas.md` :

```
## Dette technique (observée pendant le refactor SSH v1)

*(Things we saw but did not fix, to keep v1 atomic. v2 starts from this
explicit list — nothing stays implicit.)*
```

Cette section est peuplée **au fur et à mesure** du refactor (pas à la fin) :
dès qu'on hésite sur « est-ce qu'on fixe ça maintenant ? » et que la réponse
est non, on l'écrit ici. v2 partira d'une liste explicite plutôt que de
redécouvrir la dette en pleine tempête SSH.

### Dette déjà identifiée (à écrire dans la section au commit 0)

1. **`CleanupWorktrees` (`worktree_ops.go:171`)** — lance `git worktree list`
   **sans `-C`** (opère sur cwd) et `git branch -D` sans contexte repo. Bug
   latent multi-repo, pré-existant. Pas fixé en v1 (on attend v2 pour voir
   émerger les vrais besoins : cleanup distant, multi-repo, multi-host — la
   forme correcte dépend du package `Host` qui n'existe pas encore).
2. **Couplage `gh` (GitHub CLI) dans `PushChanges` / `OpenBranchURL` /
   `checkGHCLI`.** Rend boulez inopérant sur GitLab/local-host. Vrai problème,
   mais c'est un **autre feature** ("support non-GitHub"), pas du SSH. Ouvert
   maintenant = scope creep. Noté pour ne pas l'oublier ; un plan séparé le
   traitera.
3. **`worktree_branch.go` = 1 fonction (`combineErrors`).** Module trop peu
   profound. À fusionner dans `worktree_ops.go` ou `worktree_git.go` pendant
   v1 si on touche ces fichiers, sinon reporter.

---

## Critères de succès v1 (vérifiables)

1. `go build ./...` et `go test ./...` verts après chaque commit.
2. **Aucun changement de comportement** : depuis l'extérieur, boulez se comporte
   exactement comme avant (toutes les opérations git/FS passent par
   `LocalFS`/`Exec` local via les constructeurs par défaut).
3. `session/git/` ne contient plus **aucun** appel direct à
  `exec.Command("git", ...)` hors des constructeurs de test / faux — tout
   passe par `g.cmdExec` ou `r.cmdExec` (Repo).
4. `session/git/` et `session/instance.go` ne contiennent plus d'appels
   `os.Stat`/`os.RemoveAll`/`os.MkdirAll`/`os.ReadDir` sur `worktreePath`
   hors de `LocalFS` — tout passe par `g.fs`.
5. Le type `Repo` existe, porte `FetchBranches`/`SearchBranches`/`Root`/
   `BranchExists`, et le branch-picker l'utilise (plus de fonctions
   package-level prenant un `repoPath` string nu).
6. Deux **fausses** implémentations (faux `Executor`, faux `FS`) existent en
   test et prouvent que git + FS sont routables sans SSH. C'est la garantie
   que v2 est un ajout pur, pas un refactor supplémentaire.
7. `program.Adapter` n'a **pas été touché** (validation : le diff de
   `program/` est vide).
8. La section « Dette technique (observée pendant le refactor SSH v1) »
   existe dans `roadmap_and_ideas.md` et liste au minimum les 3 items
   ci-dessus.

---

## v2 — Aperçu (hors scope de ce document, objet d'un plan séparé)

Ce que v1 rend possible, dans l'ordre probable :

1. **Package `Host`.** Bundle `Executor` + `FS` + `PtyFactory` derrière une
   interface `Host`. `LocalHost` (comportement d'aujourd'hui) +
   `SSHHost` (wrap via `ssh host ...`). `Instance` tient un `Host` au lieu de
   trois champs séparés. La connaissance du transport vit à **un seul
   endroit** (module profond, DRY/SRP).
2. **Couplage path generation.** `getWorktreeDirectory()` /
   `resolveWorktreePaths` retourne un path **local** aujourd'hui. v2 doit
   demander au `Host` son répertoire de worktrees (le `~/.boulez/worktrees` du
   distant, résolu via une commande `ssh host sh -c 'echo $HOME'` cacheable,
   ou en gardant des paths `~`-relatifs que le shell distant étend).
3. **Registre d'hosts + sélecteur.** `host.Registry` (miroir de
   `repo.Registry`), `~/.boulez/hosts.json`, `HostSelector` (copie du
   `RepoSelector`). Flow de création : `host → repo-sur-ce-host → branch`.
4. **`InstanceData.Host`.** Persistance du host sur l'instance. À la
   restauration, lookup du host → injection du bon `Host` (Local ou SSH).
5. **AutoYes par-instance + policy par host.** Rendre AutoYes *vraiment*
   par-instance : le daemon ne force plus `true` globalement, il respecte le
   flag par-instance. `Host.AutoYesDefault()` (Local → suit le flag global
   actuel ; SSH → `false`). Toggle par-instance facile + badge
   d'avertissement quand AutoYes ON sur un host distant.
6. **Attach distant.** `SSHHost.Attach()` via une `PtyFactory` qui lance
   `ssh host -t tmux attach -t <session>`. Le seam `PtyFactory` existe déjà.
7. **Sécurité / PII.** Validation des `repoPath` saisis librement (paths
   avec espaces/quotes via ssh-shell) ; les `worktreePath` générés par boulez
   restent contrôlés (`sanitizeBranchName` + hex). Hostnames jamais dans
   commit messages / noms de branche / noms de session tmux.
8. **`CleanupWorktrees` rendu host+repo-aware** (corrige le bug latent
   multi-repo + active le cleanup distant). La forme précise émergera des
   besoins v2 observés (dette #1).
9. **Préconditions.** Le binaire de l'agent doit exister sur le distant
   (comme « tmux installé » aujourd'hui). Port-forwarding des dev servers
   = à l'utilisateur en v2 (vs VS Code qui auto-forwarde).

---

## Ce qui est explicitement hors scope de v1

- **Transport SSH.** Aucun `ssh` invoqué. Aucune `SSHHost`.
- **Package `Host`.** Pas de bundle spéculatif (décision 6).
- **AutoYes par-instance / policy par host.** Comportement AutoYes
  inchangé (décision 3 implémentée en v2).
- **Registre d'hosts, sélecteur d'host, `InstanceData.Host`.**
- **Path generation distante** (`getWorktreeDirectory` local en v1).
- **`CleanupWorktrees`** (bug latent multi-repo, dette #1, noté).
- **Support non-GitHub / découplage `gh`** (dette #2, noté, autre feature).
- **TUI** (rendu du sélecteur d'host, badges, avertissements AutoYes).
