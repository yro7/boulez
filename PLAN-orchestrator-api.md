# Plan : API de contrôle (kernel + syscalls) pour l'orchestrateur cs2

> Objectif : exposer une **API de contrôle bas niveau** — pensée comme les
> syscalls d'un OS — qui permet à un cerveau (LLM ou non) de superviser toute
> la flotte cs2 : spawner des instances, leur donner des tâches, merger des
> branches, résoudre les conflits.
>
> **Scope strictement l'API (Shape A).** Le branchement d'un LLM consommateur
> (Shape B) vient après, dans un plan séparé. cs2 ne connaît jamais le LLM :
> l'API est consommateur-agnostique, exactement comme `program.Adapter`.
>
> Ce plan construit le socle sur lequel un futur "agent orchestrateur" (LLM)
> se branchera. L'orchestrateur *est* une instance d'agent ordinaire qui
> consomme ces syscalls via des tools — il n'a rien de spécial côté cs2.

---

## Métaphore OS (la grille de lecture verrouillée)

- **Le daemon = le kernel.** Processus long-running déjà existant. Il
  **possède** l'état mutable de la flotte et est le **seul writer** autorisé
  à le modifier. Aujourd'hui il ne fait que poller l'auto-yes ; on l'étend
  en autorité de contrôle.
- **`cs2 ctl` = l'interface syscall.** Client mince qui envoie des requêtes
  typées au kernel. Les tools exposés à un LLM seront une fine couche de
  description au-dessus de `cs2 ctl` — cs2 ne sait pas qu'un LLM l'appelle.
- **Les instances = processus** gérés par le kernel.
- **La TUI = console** : observateur en lecture, plus autorité d'écriture
  pour les opérations de contrôle (elle peut toujours faire ce qu'elle fait
  aujourd'hui pour ses propres instances locales, mais le chemin canonical
  de contrôle passe par le kernel).

**Conséquence de structure :** un seul writer mutable (le daemon). `cs2 ctl`
n'écrit **jamais** le storage directement ; il dépose une requête et lit la
réponse. Évite le piège classique du troisième writer concurrent (TUI +
daemon + ctl) qui rendrait l'état non-déterministe.

---

## Décisions verrouillées (issues de la discussion)

1. **Shape A d'abord, Shape B ensuite.** On construit l'API déterministe ;
   le LLM viendra la consommer plus tard. cs2 reste agent-agnostic pur.
2. **`Instance.Kind` ∈ {Worker, Orchestrator}.** Le `Kind` est le **point
   d'extension** pour la hiérarchie future : aujourd'hui Worker ne peut pas
   spawn, Orchestrator peut. Lever plus tard la restriction « Orchestrator
   peut spawner des sous-orchestrateurs » = un changement de garde, pas un
   refacto. La hiérarchie *super-orchestrateur → n orchestrateurs → m workers*
   se déduit naturellement et est explicitement différée.
3. **Worktree polymorphe, pas de garde `if kind == ...` éparpillé.** Le
   worktree devient une interface avec deux implémentations : `realWorktree`
   (Worker, actuel) et `headlessWorktree` (Orchestrator, no-op pour toute
   opération git). L'amendement à l'invariant AGENTS.md :
   > *Tout **worker** est lié à un worktree. L'orchestrateur ne l'est pas —
   > il supervise la flotte.* La *raison* de l'invariant (isoler les agents
   > qui éditent du code) ne s'applique pas à un superviseur qui n'édite pas.
4. **Handle universel = `Instance.ID`** (persistant), pas le Title (mutable
   et non unique). Toute la surface API parle en ID.
5. **Concurrence : daemon = seul writer mutable.** `cs2 ctl` = file/requête
   synchrone vers le daemon via socket unix.
6. **Scope du `merge` : n'importe quel repo du registre** (`repo.Registry`),
   pas seulement les repos ayant des workers actifs.
7. **Gardes de sécurité côté kernel, non-contournables par le client.** Le
   daemon refuse une branche protégée (`main` / branche courante du repo
   hôte) peu importe ce que le client demande. Le mode « approbation humaine »
   est un garde parmi d'autres, côté serveur (application directe de
   git-guardrails à l'API de contrôle).
8. **Merge v1 = abstraction `GitMerger` quasi vide :** auto-merge
   déterministe, **fail propre si conflit réel**. Les instances
   `GitMergeConflictResolver` (Shape B, un worker dont la tâche = résoudre)
   viennent dans un plan ultérieur.
9. **Persistence de la flotte + état du plan.** L'orchestrateur est restauré
   au redémarrage de cs2 : un LLM peut « reprendre » un plan interrompu.

---

## Choix du transport (alternatives envisagées, choix retenu)

| Option | Avantage | Inconvénient |
|---|---|---|
| **(a) CLI flags `cs2 ctl spawn ...` qui écrit le storage** | Très simple, zéro IPC | 3e writer concurrent (TUI+daemon+ctl) → races ; pas de retour synchrone (l'LLM n'obtient pas l'ID du spawn) |
| **(b) File de requêtes JSON sur disque (`~/.cs2/requests/`)** | Simple, pas de serveur | Asynchrone : le client doit poller l'état pour le résultat — mauvais pour un LLM qui a besoin de l'ID de l'instance créée |
| **(c) Socket unix + JSON-RPC (newline-delimited), daemon = serveur** ✅ | Synchrone (req→resp), un seul writer, testable in-process (kernel = fonctions Go pures, socket = couche fine) | Un peu plus de plomberie ; le daemon doit être up |

**Retenu : (c).** Raisons : (i) un LLM a besoin de réponses synchrones
(`spawn` → `{id}`), (ii) le kernel reste des fonctions Go testables sans
socket ni tmux, (iii) `cs2 ctl` est un client trivial au-dessus de la même
socket — un seul transport, deux faces (programmatic + humain). Le coût
(plomberie socket) est payé une fois et amorti par tous les syscalls.

Détail lifecycle : si `cs2 ctl` trouve le daemon down, il l'auto-lance
(`daemon.LaunchDaemon`) puis réessaie. Le daemon est désormais le
processus canonique « toujours up pendant qu'on utilise cs2 ».

---

## Table des syscalls (spécification v1)

Toute entrée/sortie est JSON. Les erreurs sont structurées :
`{"error": "...", "code": "PROTECTED_BRANCH"|"UNKNOWN_INSTANCE"|...}`.

### Lecture (observabilité — first-class pour la décision LLM)

| Syscall | Entrée | Sortie |
|---|---|---|
| `list_instances` | `{filter?: {kind?, repo?, status?}}` | `[{id, kind, status, repo, branch, title, updated_at}]` |
| `get_instance` | `{id}` | `{id, kind, status, repo, branch, title, diff, log, updated_at}` (`diff` = stats + content ; `log` = scrollback tmux) |

### Mutation (cycle de vie)

| Syscall | Entrée | Sortie |
|---|---|---|
| `spawn_worker` | `{repo, branch?, prompt, program?, title?}` | `{id}` |
| `send_prompt` | `{id, prompt}` | `{ok}` |
| `pause` | `{id}` | `{ok}` |
| `resume` | `{id}` | `{ok}` |
| `kill` | `{id}` | `{ok}` |

### Orchestration

| Syscall | Entrée | Sortie |
|---|---|---|
| `merge` | `{target_repo, target_branch, source_branches[], strategy?}` | `{result: "merged"\|"conflict", conflicts?: [{file, ours?, theirs?}]}` |

**Garde `merge` :** `target_branch` ne doit pas être une branche protégée
(`main`, `master`, ou la branche courante du repo hôte). Refus côté kernel,
code `PROTECTED_BRANCH`. (Différé : liste de branches protégées configurable.)

**`spawn_worker` garde de récursion :** refus si l'appelant est lui-même un
Worker. En v1 l'appelant est identifié via l'instance-orchestrateur qui
détient le canal ; la topologie est strictement 2 niveaux. Le `Kind` est le
point d'extension pour la hiérarchie future.

---

## Modèle de données (changements)

### `Instance.ID` (nouveau, persistant)

UUID v4 ou monotonic counter. Géné à la création, jamais muté, jamais
réutilisé. **Handle universel de l'API.** Ajouté à `InstanceData` (champ
`id`), backfillé (vide = instance pré-existante, alloué au load).

### `Instance.Kind` (nouveau)

```go
type Kind int
const (
    KindWorker Kind = iota
    KindOrchestrator
)
```
Persisté dans `InstanceData.Kind`. Défaut `KindWorker` (back-compat).

### Worktree polymorphe

Introduction d'une interface (nom de travail `Worktree`) qui factorise le
surface actuellement portée par `*git.GitWorktree`. Deux implémentations :

- `realWorktree` : comportement actuel, délègue à `git.GitWorktree`.
- `headlessWorktree` : no-op pour `Setup`/`Cleanup`/`Remove`/`CommitChanges`/
  `IsDirty`/etc. ; `GetWorktreePath` retourne le **dir de contrôle**
  (`~/.cs2/orchestrators/<id>/`), pas un path git.

**Règle deep-module : aucune fonction d'`Instance` ne fait `if kind == ...`.**
La distinction vit derrière l'interface Worktree. `Instance.Start` bind le
bon Worktree selon `Kind` à un seul endroit (le factory).

> Note d'implémentation : introduire l'interface est un refacto de
> **découverte** — on extrait l'interface existante de `*git.GitWorktree`
> sans changer son comportement, puis on ajoute `headlessWorktree`. Fait en
> deux commits pour garder le refacto reviewable et le diff sémantique isolé.

### Entité `Orchestrator` (état du plan, persistée)

L'orchestrateur (cerveau LLM) est lui-même une `Instance` de `KindOrchestrator`.
Son **plan** (la liste des workers qu'il a spawnés + l'état du merge) est
persisté à part dans `~/.cs2/orchestrators/<id>/plan.json` :

```json
{
  "id": "<orchestrator-id>",
  "worker_ids": ["<id>", ...],
  "merge_targets": [{"repo": "...", "branch": "..."}],
  "state": "running" | "merging" | "done" | "failed"
}
```

Restauré au démarrage. C'est ce qui rend un plan « reprendable ».

---

## Abstraction `GitMerger` (v1 : quasi vide)

Nouveau package `session/git/merge/` (ou fichier dans `session/git/`).

```go
type MergeResult struct {
    Status    MergeStatus // Merged | Conflict
    Conflicts []Conflict  // vide si Merged
}
type Conflict struct {
    File   string
    Ours   string // version côté target, si applicable
    Theirs string // version côté source
}
type Strategy int // StrategyDefault (= git merge) en v1 ; ours/theirs plus tard

type Merger interface {
    Merge(targetRepo, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error)
}
```

**v1 :** implémentation `defaultMerger` qui fait `git -C <repo> checkout
<target> && git merge <sources...>`, parse la sortie / le status pour
détecter les conflits, et **échoue proprement** (retourne `Status: Conflict`,
laisse le repo dans un état fusionnable par un resolver futur — ne force
**jamais** `--abort` silencieux). Repo-aware (utilise `git -C`, déjà le
pattern v2).

Tests : merge propre (2 branches disjointes), conflit détecté (même ligne
modifiée), branche protégée refusée, multi-repo (cwd neutre).

**Différé (plan ultérieur, Shape B) :** `GitMergeConflictResolver` = un
Worker spawné avec un prompt décrivant le conflit + les noms de branches +
le diff. C'est la résolution *agentique* — la résolution de conflit *est
elle-même* une tâche agentique. cs2 fournit la mécanique, le cerveau décide.

---

## Étapes (commits atomiques, chacun vert + testé)

Chaque étape est indépendante, compile, et passe `go build ./... && go test
./...`. Aucune étape ne dépend d'un LLM.

### Étape 1 — `Instance.ID` (handle universel)

**Décision couverte :** 4.

- `session/instance.go` : champ `ID string` sur `Instance` ; généré
  (`crypto/rand` uuid v4) dans `NewInstance` si vide.
- `session/storage.go` : champ `ID` sur `InstanceData` ; `ToInstanceData` /
  `FromInstanceData` le portent.
- Backfill : `FromInstanceData` alloue un ID si le champ persisté est vide
  (instances pré-existantes).
- `Instance.ID()` getter.

Tests : ID alloué à la création, stable au round-trip (save→load), backfill
des instances sans ID.

**Commit :** `feat(session): add stable Instance.ID as universal handle`

### Étape 2 — `Instance.Kind` + worktree polymorphe

**Décisions couvertes :** 2, 3.

- `session/instance.go` : `Kind` + constantes ; champ persisté dans
  `InstanceData.Kind` (défaut `KindWorker`).
- Introduction de l'interface `Worktree` (surface extraite de
  `*git.GitWorktree`) en deux commits :
  - 2a — **refacto pur** : `Instance.gitWorktree` devient le type interface
    `Worktree` ; `*git.GitWorktree` la satisfait ; aucun comportement
    changé. Tout test existant reste vert.
  - 2b — `headlessWorktree` : implémentation no-op, `GetWorktreePath` →
    dir de contrôle `~/.cs2/orchestrators/<id>/`.
- `Instance.Start` : factory à un seul endroit choisit `realWorktree` vs
  `headlessWorktree` selon `Kind`. **Zéro `if kind == ...` ailleurs.**

Tests : Worker démarre comme avant (smoke) ; Orchestrator démarre avec
worktree no-op, `GetWorktreePath` = dir de contrôle, `IsDirty` = false.

**Commits :**
- `refactor(session): extract Worktree interface from git.GitWorktree`
- `feat(session): add Kind + headlessWorktree for Orchestrator instances`

### Étape 3 — `Spawn` non-interactif (chemin programmatique)

**Décision couverte :** 1 (socle), 4.

- `app/spawn.go` (ou méthode sur un nouveau `app.Kernel`) :
  ```go
  type SpawnOptions struct {
      Repo    string
      Branch  string // optionnel
      Prompt  string
      Program string // optionnel, défaut global
      Title   string // optionnel
      Kind    session.Kind
  }
  func (k *Kernel) Spawn(opts SpawnOptions) (string, error) // retourne l'ID
  ```
  Réutilise `session.NewInstance` + `Start`, sans aucune TUI.
- **Garde de récursion :** `Spawn` refuse `KindWorker` si l'appelant est
  lui-même un Worker (en v1 : paramètre `callerKind` explicite ; la
  topologie 2-niveaux est codée dure ici, au seul point de spawn).

Tests : spawn local (repo existant, branche nouvelle), spawn avec prompt
initial, spawn avec branche existante, erreur (repo inexistant, branche
absente), garde de récursion (Worker → spawn Worker = erreur).

**Commit :** `feat(app): add programmatic Spawn (non-interactive instance creation)`

### Étape 4 — Abstraction `GitMerger` (v1 déterministe)

**Décision couverte :** 6, 8.

- `session/git/merge.go` : interface `Merger` + types `MergeResult`/
  `Conflict`/`Strategy` + implémentation `defaultMerger`.
- `defaultMerger.Merge` : `git -C <repo> checkout <target>` puis
  `git merge <sources...>` ; parse conflits via `git status --porcelain`
  et `git diff --name-only --diff-filter=U`. **N'aborte jamais** — laisse
  le repo dans l'état pour un resolver futur.
- **Garde branche protégée** : refus `main`/`master`/branche courante du
  repo hôte, erreur typée `PROTECTED_BRANCH`.

Tests : merge propre, conflit détecté, branche protégée refusée, multi-repo
(cwd neutre via `-C`), source inexistante = erreur propre.

**Commit :** `feat(git): add Merger abstraction with deterministic v1 + protected-branch guard`

### Étape 5 — Kernel (daemon) : autorité de contrôle

**Décisions couvertes :** 5, 7 (les gardes vivent ici).

- Nouveau package `kernel/` (ou `app/kernel.go`) : type `Kernel` qui
  **possède** le storage + la liste d'instances en mémoire, et expose les
  syscalls comme **méthodes Go pures** (pas d'IPC) :
  `ListInstances`, `GetInstance`, `Spawn`, `SendPrompt`, `Pause`, `Resume`,
  `Kill`, `Merge`. Chaque garde de sécurité vit ici (branche protégée,
  récursion, instance inconnue).
- Le daemon existant est étendu : il instancie un `Kernel`, charge les
  instances persistées, et **boucle** (poll + applique les requêtes entrantes).
- La boucle d'orchestration (attendre que tous les workers d'un plan soient
  `Ready` puis lancer le merge) est un sous-ensemble — **différé en v1.1** ;
  v1 se contente d'exposer `Merge` à la demande du client.

Tests : tous les syscalls testables **sans socket ni tmux** (le kernel est
des fonctions ; on injecte des fakes repo/worktree). C'est le cœur de la
testabilité, à la `program.Adapter`.

**Commit :** `feat(kernel): add Kernel as single-writer control authority with syscalls`

### Étape 6 — Transport : socket unix + `cs2 ctl`

**Décision couverte :** 5 (transport = choix (c)).

- `kernel/transport.go` : serveur JSON-RPC newline-delimited sur
  `~/.cs2/ctl.sock`. Couche fine : décode la requête → appelle la méthode
  Kernel correspondante → encode la réponse. Aucune logique métier ici.
- `cmd/ctl.go` (ou `main.go` subcommand) : `cs2 ctl <syscall> [flags]`.
  Client mince : construit le JSON, l'envoie, affiche le JSON (ou formaté
  avec `--json` vs humain).
- Auto-launch du daemon si la socket est absente (`daemon.LaunchDaemon`
  puis retry).

Tests : transport testé via un kernel en mémoire (in-process) ; round-trip
req→resp pour chaque syscall ; daemon-down → auto-launch → retry.

**Commit :** `feat(kernel): add unix socket transport + cs2 ctl client`

### Étape 7 — Persistence de l'orchestrateur (plan reprendable)

**Décision couverte :** 9.

- `~/.cs2/orchestrators/<id>/plan.json` : struct `OrchestratorPlan`
  (`worker_ids`, `merge_targets`, `state`).
- Le Kernel persiste le plan à chaque mutation (spawn d'un worker → ajoute
  à `worker_ids` ; merge → transition d'état).
- Au démarrage, le Kernel restaure les plans `running`/`merging` et les
  réexpose via `list_instances` (filter `kind=orchestrator`).

Tests : save→load d'un plan, reprise d'un plan `merging`, un plan `done`
n'est pas réexécuté.

**Commit :** `feat(kernel): persist Orchestrator plans for resumable supervision`

---

## Explicitement différé (hors scope v1)

- **Shape B : branchement LLM.** Un adapter (Pi, Claude, …) expose
  `cs2 ctl` comme outils à son agent. Vit dans `program/`, un fichier par
  agent — cs2 ne sait pas qu'un LLM l'appelle. Plan séparé.
- **Hiérarchie > 2 niveaux.** `super-orchestrateur → n orchestrateurs →
  m workers`. Le `Kind` + la garde de récursion (étape 3) sont le point
  d'extension : lever la restriction = changer la garde, pas l'archi.
- **`GitMergeConflictResolver`** (résolution agentique des conflits).
  cs2 fournit la mécanique (étape 4), un worker resolver est spawné avec
  un prompt. Plan Shape B.
- **Boucle d'orchestration autonome** (kernel attend tous les workers
  `Ready` puis merge automatiquement selon une stratégie). v1.1.
- **Résolution de conflit non-déterministe** (stratégies ours/theirs,
  LLM-lit-le-diff). `Strategy` est prévu dans l'interface (étape 4) mais
  seul `StrategyDefault` est implémenté en v1.
- **Liste de branches protégées configurable** (au-delà de
  `main`/`master`/courante).
- **Multi-repo dans un seul plan** (un orchestrateur pilotant des merges
  sur plusieurs repos simultanés). `merge` est déjà repo-quelconque (étape 4,
  décision 6), mais l'orchestration *coordonnée* multi-repo est différée.

---

## Invariants structurels (à pinner par des tests)

1. **Un seul writer mutable = le Kernel.** `cs2 ctl` et la TUI ne touchent
   jamais le storage en écriture directe.
2. **Aucun `if kind == ...` hors du factory de `Start`.** La distinction
   Worker/Orchestrator vit derrière l'interface `Worktree`.
3. **Aucun ID muté après création.** `Instance.ID` est immutable.
4. **`merge` refuse une branche protégée, côté kernel, peu importe le
   client.** Non-contournable.
5. **`Merger` n'aborte jamais silencieusement** — un conflit laisse le repo
   dans un état fusionnable, pas un état effacé.
6. **cs2 ne connaît aucun LLM.** L'API est consommateur-agnostique.
