# Plan : Corrections & durcissement de l'API de contrôle cs2

> Suite du **`PLAN-orchestrator-api.md`** (Shape A, livrée). Ce plan corrige
> les bugs trouvés lors du dogfooding et ajoute les fonctionnalités
> nécessaires **avant** d'ouvrir l'API à un consommateur non-CLI (Shape B :
> un LLM pilotant la flotte). L'API reste consommateur-agnostique.

## Origine : dogfooding du socle Shape A

Tous les points ci-dessous sont issus d'une session de smoke-test sur
`cs2 ctl` (socket unix `~/.cs2/ctl.sock`). Les 8 commits de Shape A sont
verts et fonctionnels ; ce plan s'attaque aux angles morts découverts.

## Résumé des décisions verrouillées

1. **Le JSON de `cs2 ctl` doit être parsable sans post-traitement.** Aucune
   sortie parasite sur stdout. (Fix #6)
2. **La garde « branche courante du repo hôte » est obligatoire, côté kernel,
   non-contournable.** Elle faisait partie des décisions verrouillées de Shape A
   (décision 7) mais n'a jamais été implémentée. (Fix #3)
3. **L'identité du caller est dérivée par le transport, pas déclarée par le
   client.** En v1 l'identité = la connexion (1 socket = 1 session
   authentifiée). Le paramètre `caller` devient read-only côté client. (Fix #7)
4. **Le daemon n'est lancé qu'une fois, via un lock fichier.** L'auto-launch
   concurrent ne multiplie plus les daemons. (Fix #5)
5. **`cs2 ctl` expose `--caller-id` (optionnel) pour les plans.** Mais
   l'autorité reste au kernel, qui **valide** que l'ID existe et a le `Kind`
   revendiqué. Un worker ne peut pas se faire passer pour un orchestrator.
6. **Le wire JSON utilise des ints pour `Kind`/`Status`, mais un
   `TextUnmarshaler` accepte aussi les strings** pour la robustesse cross-tool.
   (Fix #8)
7. **`spawn_worker --branch X` crée la branche si elle n'existe pas** (au lieu
   d'échouer), sauf si `--branch-existing` est passé. Comportement par défaut
   aligné sur l'usage orchestrateur (noms déterministes). (Fix #1)

Les points mineurs (#2 enums en string dans la sortie, #4 plans invisibles en
CLI) sont traités en passant dans les étapes concernées — pas d'étapes dédiées.

---

## Étapes (commits atomiques, chacun vert + testé)

Chaque étape compile et passe `go build ./... && go test ./...`. Aucune étape
ne dépend d'un LLM. L'ordre suit la priorité consommateur (impact parsing >
sécurité > robustesse > ergonomie).

### Étape 1 — Sortie `cs2 ctl` strictement JSON (Fix #6)

**Décision :** 1.

**Problème :** `log.Close()` imprime `wrote logs to /tmp/.../claudesquad.log`
sur **stdout** via `fmt.Println` à chaque appel `cs2 ctl`. La ligne est collée
après le JSON de réponse → tout parseur JSON stricte casse
(`Extra data: line 14 column 1`). Reproduit : `cs2 ctl get_instance | python3
-m json.tool` échoue.

**Changement :**
- `log/log.go` : `Close()` n'imprime plus sur stdout. Le message devient
  optionnel via un booléen `verbose` (par défaut `false`). Le path du log
  reste disponible via un getter `LogFilePath() string` pour qui veut
  l'afficher (ex. le mode interactif TUI, ou `--verbose`).
- `cmd_ctl.go` : `rawCtl` ne dépend plus de l'effet de bord d'affichage de
  `log.Close()` ; le path du log n'est jamais imprimé en mode `ctl`
  (machine-facing). En cas d'erreur fatale, le message d'erreur stdout/stderr
  reste JSON.

**Tests :**
- `log` : `Close()` n'écrit rien sur stdout (capture via `os.Pipe`).
- `cmd` (test existant `cmd/cmd_test`) : le stdout d'un `ctl` round-trip
  contient **exactement** un document JSON (un seul `}`, pas de ligne
  parasite). Ajouter un test `TestCtl_StdoutIsPureJSON` qui parse la sortie.

**Commit :** `fix(log): keep ctl stdout pure JSON (no log-path line)`

---

### Étape 2 — Garde « branche courante du repo hôte » (Fix #3)

**Décision :** 2.

**Problème :** La spec Shape A (décision 7, étape 4) exigeait que `merge`
refuse `main`, `master` **et la branche courante du repo hôte**. Seuls
`main`/`master` sont implémentés (`session/git/merge.go` :
`protectedBranches` map statique). `merge --target-branch integration` où
`integration` est la branche courante du repo a **réussi** — écart spec réel,
non-contournable côté sécurité.

**Changement :** la garde doit connaître la branche courante du **repo cible**.
Deux implémentations défendent en profondeur :

- `session/git/merge.go` : `defaultMerger.Merge` calcule la branche courante
  via `git -C <repo> rev-parse --abbrev-ref HEAD` et la traite comme protégée
  (en plus de `main`/`master`). Nouveau type d'erreur typé
  `ErrProtectedBranch` réutilisé (déjà mappé à `PROTECTED_BRANCH` côté wire).
  - Subtilité : après `git checkout <targetBranch>`, la branche courante
    **devient** `targetBranch`. La vérification se fait **avant** le checkout,
    sur l'état initial du repo. Documenter ce fait.
- `kernel/kernel.go` `Merge` : garde miroir côté kernel (defense in depth,
  comme déjà fait pour `main`/`master` via le Merger injecté). Le kernel
  n'appelle pas git directement ; il délègue au Merger qui porte la garde.

**Tests** (`session/git/merge_test.go`, étendre) :
- `TestMerger_RefusesCurrentBranch` : repo dont la branche courante est
  `integration`, on tente `merge --target-branch integration` →
  `ErrProtectedBranch`. (C'est précisément le cas qui passait à tort.)
- Le test existant `TestMerger_ProtectedBranchRefused` (main) reste vert.

**Commit :** `fix(git): enforce current-branch guard in Merger (spec decision 7)`

---

### Étape 3 — Auto-launch du daemon sans course (Fix #5)

**Décision :** 4.

**Problème :** Sous un storm de `cs2 ctl` concurrents avec daemon down,
plusieurs clients trouvent la socket absente et **chacun lance son propre
daemon** (jusqu'à 5+ processus observés). Pas de lock, pas de détection
« daemon en cours de lancement ».

**Changement :** `daemon.LaunchDaemon` prend un **fichier lock**
(`~/.cs2/daemon.lock`, via `flock`-style `O_EXCL` + PID dedans) :
- Si le lock est déjà détenu (fichier existe + PID vivant), `LaunchDaemon`
  retourne `nil` (un daemon est déjà en route ou démarre) — ne lance pas de
  second processus.
- Si le fichier existe mais le PID est mort, le reclaim (supprime le lock,
  prend la place).
- Le daemon, au démarrage (`RunDaemon`), écrit son PID dans le lock et le
  supprime au shutdown (déjà partiellement fait via `daemon.pid` — on unifie
  `daemon.pid` et le lock en un seul fichier `daemon.lock`).
- `cmd_ctl.go` `rawCtl` : après `LaunchDaemon`, attendre activement la socket
  (poll ~50ms, timeout ~2s) avant de réessayer l'appel, plutôt qu'un `sleep`
  implicite. Évite qu'un client retry avant que le daemon ait bind.

**Subtilité concurrence :** `O_EXCL` atomique côté création du lock ; le check
de PID vivant gère le cas d'un lock stale d'un crash. Le race window
(fichier créé mais PID pas encore écrit) est couvert par : si le lock existe,
on n'en lance pas de nouveau, point (le détenteur va finir d'écrire son PID).

**Tests** (`daemon/`, nouveau fichier de test — package actuellement sans
tests) :
- `TestLaunchDaemon_SingleInstance` : deux appels concurrents à
  `LaunchDaemon` (avec un faux exécutable / mock) ne lancent qu'un seul
  processus.
- `TestLaunchDaemon_ReclaimStaleLock` : un lock avec un PID mort est reclaimé.

**Commit :** `fix(daemon): single-instance launch via lock file + socket wait`

---

### Étape 4 — Caller authentifié par le transport (Fix #7)

**Décision :** 3, 5.

**Problème :** Le paramètre `caller` du RPC est **fourni par le client et non
vérifié**. J'ai pu me faire passer pour n'importe quelle instance en forgeant
le JSON (spoof de `caller.id`/`caller.kind`), contournant les gardes
`WORKER_CANNOT_SPAWN` / `NESTED_ORCHESTRATOR`. Inoffensif en v1 (CLI
mono-utilisateur local), mais un trou avant Shape B.

**Modèle :** « une socket = une session = une identité ».
- Le transport (`kernel/transport.go`) maintient un **session ID** par
  connexion (UUID généré à l'`Accept`).
- Une connexion démarre **non-authentifiée** (top-level, comme `cs2 ctl`
  aujourd'hui — autorisé à tout faire).
- Nouveau syscall **`authenticate`** : `{instance_id, kind}`. Le kernel
  **valide** que l'instance existe, **vérifie** son `Kind`, et **bind** la
  session à cette identité. À partir de là, tout `caller` est dérivé de la
  session, ignoré côté params.
- Les `spawnParams.caller` / `mergeParams.caller` deviennent **ignorés** côté
  serveur (ou supprimés du wire). Le `CallerContext` est construit par le
  transport à partir de la session, jamais depuis le JSON client.
- **Garde de vérification :** un worker authentifié ne peut pas spawn
  (déjà `IsWorker()`), un orchestrator ne peut pas spawn un orchestrator
  (déjà `ErrNestedOrchestrator`). La nouveauté : on ne peut plus **mentir**
  sur son Kind pour esquiver la garde.

**Validation du binding :** comment le kernel sait qu'une session a le droit
de se lier à l'instance X ? En v1 (local, mono-utilisateur), toute connexion
locale au socket unix (mode `0o600`, propriétaire only) est **trustée** pour
s'authentifier comme n'importe quelle instance — l'isolation est déjà au
niveau de l'OS (seul le propriétaire peut ouvrir le socket). On documente ce
modèle de confiance local ; l'authentification forte (token par instance,
capacité) est différée à Shape B multi-consommateur.

**Changement concret :**
- `kernel/transport.go` : `handleConn` crée un `*Session` (id + caller
  résolu). `dispatch` reçoit le `*Session` et en tire le `CallerContext`.
  Nouveau case `"authenticate"` → `k.BindSession(sessionID, instanceID)`.
- `kernel/kernel.go` : `Kernel` garde une `map[sessionID]CallerContext`.
  `Spawn`/`Merge` reçoivent le `CallerContext` du transport, plus jamais
  celui des params. Ajout d'une méthode `BindCaller(sessionID, instanceID)
  error` qui valide l'instance + son Kind et stocke le binding.
- `cmd_ctl.go` : pas de `--caller` direct ; si l'utilisateur veut agir « en
  tant qu'orchestrateur X », nouveau sous-mode `cs2 ctl as <id> <syscall ...>`
  qui appelle `authenticate` puis le syscall dans la même session (une
  connexion, deux messages : authenticate + la commande). Réouvre le
  dogfooding des plans (#4) via le CLI officiel.

**Tests** (`kernel/`) :
- `TestTransport_CallerFromSession` : un client qui ne s'authentifie pas est
  top-level (peut spawn).
- `TestTransport_AuthenticateAsWorker` : après `authenticate` sur un worker,
  `spawn_worker` → `WORKER_CANNOT_SPAWN` (et non contournable en mentant sur
  `caller.kind`).
- `TestTransport_AuthenticateAsOrchestrator` : après auth comme
  orchestrator, spawn d'un worker **enregistre le plan** (valide que
  l'étape 7 est enfin atteignable en CLI via `cs2 ctl as`).
- `TestTransport_SpoofedCallerIgnored` : params avec `caller` forgé sont
  ignorés ; le caller réel (session) prime.

**Commit :** `feat(kernel): authenticate caller via transport session, not client params`

---

### Étape 5 — `Kind`/`Status` acceptent string ou int sur le wire (Fix #8)

**Décision :** 6.

**Problème :** Le flag CLI `--kind orchestrator` est converti en int côté
client (`kindInt()`), mais le wire attend un `session.Kind` (int via iota).
Un appelant JSON-RPC direct qui passe `"kind": "orchestrator"` (string)
obtient `INTERNAL: cannot unmarshal string`. Incohérent entre client CLI
(string) et wire nu (int-only).

**Changement :**
- `session/instance.go` : `Kind` et `Status` implémentent
  `json.Unmarshaler` (acceptent `"worker"`/`"orchestrator"` et `0`/`1`) et
  `json.Marshaler` (sortent en **string** — règle #2 résolue en passant :
  la sortie devient `"status": "paused"` au lieu de `3`, plus convivial et
  self-documenting pour un LLM).
  - Unmarshal : accepter les deux formes. `"worker"`→0, `"orchestrator"`→1,
    `0`→0, `1`→1.
  - Marshal : sortir la string lower-case (`"running"`, `"paused"`,
    `"worker"`, `"orchestrator"`).
- `cmd_ctl.go` : `kindInt`/`statusInt` deviennent inutiles (le wire accepte
  la string telle quelle). Supprimer la conversion ; passer la string
  directement. Moins de code, une seule source de vérité (le `Unmarshaler`).
- `kernel/transport.go` : `listParams.Kind *session.Kind` etc. fonctionnent
  déjà (le pointeur déclenche l'Unmarshaler).

**Back-compat :** un ancien client qui enverrait un int est toujours accepté
(côté unmarshal). La sortie change de forme (int→string) : c'est un **changement
mineur de l'API de sortie**, acceptable car Shape A n'a pas de consommateur
stabilisé en production. Documenter dans le changelog.

**Tests** (`session/`) :
- `TestKind_UnmarshalStringOrInt` : `"orchestrator"`, `1`, `0`, `"worker"`
  décodent correctement.
- `TestKind_MarshalString` : `KindOrchestrator` → `"orchestrator"`.
- Idem pour `Status`.
- `kernel` : round-trip transport avec `kind` en string et en int, les deux
  passent.

**Commit :** `feat(session): Kind/Status marshal as strings, accept string|int`

---

### Étape 6 — `spawn_worker --branch` crée la branche si absente (Fix #1)

**Décision :** 7.

**Problème :** `spawn_worker --branch feat-a` échoue si `feat-a` n'existe pas
(`INTERNAL: branch feat-a not found locally or on remote`). Sans `--branch`,
cs2 crée `cs2/spawn-<ns>`. Un orchestrateur voulant des branches déterministes
doit créer la branche git lui-même d'abord.

**Changement :**
- `app/spawn.go` : si `opts.Branch` est fournie mais n'existe pas, **la créer**
  depuis HEAD (`git branch <name> HEAD`) puis l'utiliser comme branche de
  worktree. Nouveau flag `SpawnOptions.BranchMustExist bool` (défaut `false`)
  pour l'utilisateur qui veut explicitement une branche pré-existante.
- `cmd_ctl.go` `spawn_worker` : `--branch` crée si absente (comportement par
  défaut). Nouveau flag `--branch-existing` pour exiger une branche pré-
  existante (l'ancien comportement, utile pour reprendre une branche de
  travail).
- `kernel.SpawnOptions` : propager `BranchMustExist` à travers le
  `kernel.Spawner` jusqu'à `app.Spawn`.

**Tests** (`app/spawn_test.go` ou `session/git`) :
- `TestSpawn_CreatesBranchIfAbsent` : spawn avec `--branch newfeat` où
  `newfeat` n'existe pas → succès, worktree sur `newfeat`, la branche existe
  dans le repo.
- `TestSpawn_BranchExistingFailsIfAbsent` : avec `--branch-existing`,
  branche absente → erreur propre (code `BRANCH_NOT_FOUND` ? ou `INTERNAL` —
  à décider, idéalement un nouveau code typé).

**Commit :** `feat(spawn): create branch if absent; add --branch-existing`

---

## Invariants structurels (à pinner par des tests, en plus de ceux de Shape A)

7. **Le stdout de `cs2 ctl` est un document JSON unique, parsable sans
   post-traitement.** Aucune ligne parasite. (Étape 1)
8. **Le `caller` d'un syscall vient du transport (session authentifiée),
   jamais des params du client.** Un client ne peut pas se déclarer worker
   pour contourner une garde. (Étape 4)
9. **`merge` refuse la branche courante du repo cible, côté kernel, peu
   importe le client.** Non-contournable (comme main/master). (Étape 2)
10. **Au plus un daemon tourne.** L'auto-launch ne crée jamais de second
    processus. (Étape 3)

---

## Explicitement différé (hors scope de ce plan)

- **Authentification forte multi-consommateur** (token par instance,
  capability, séparation des orchestrators concurrents). Le modèle local
  « socket unix 0o600 = trusté » suffit tant qu'un seul utilisateur utilise
  la machine. Forme Shape B+.
- **Sortie de `list_instances` avec `kind`/`status` en string ET int**
  (dual-output pour back-compat outil). On casse proprement vers le string
  (étape 5) ; pas de dual-output.
- **Boucle d'orchestration autonome** (kernel attend les workers `ready` puis
  merge) — toujours v1.1, hors scope.
- **Résolution agentique des conflits** (`GitMergeConflictResolver`) — Shape B.
- **Liste de branches protégées configurable** (au-delà de main/master/
  courante) — inchangé vs Shape A.
- **Multi-repo coordonné dans un seul plan** — inchangé vs Shape A.

---

## Ordre et dépendances

```
1 (stdout JSON)  ──┐
2 (current-branch) ─┼─> indépendants, peuvent atterrir dans n'importe quel ordre
3 (daemon lock)  ──┘

4 (caller auth)  ──> dépend conceptuellement de 1 (tests parsent le JSON)
                       mais pas techniquement ; peut se faire en parallèle

5 (Kind string)  ──> indépendant ; touche session/ + transport + ctl
6 (spawn branch) ──> indépendant ; touche app/ + cmd_ctl
```

Toutes les étapes sont testables **sans LLM ni socket réelle** (kernel =
fonctions Go pures ; transport testé in-process via une socket temporaire ;
ctl testé via capture stdout). Aucune regression sur les 8 commits Shape A :
chaque étape préserve `go build ./... && go test ./...` vert.
