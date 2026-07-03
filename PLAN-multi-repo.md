# Plan : support multi-repo (supervision centrale de dépôts indépendants)

> Objectif : faire de cs2 un panneau de supervision central d'agents tournant
> sur des **dépôts indépendants**. Aujourd'hui cs2 est codé « cwd = le repo
> source unique ». On supprime ce couplage : cwd n'a plus de rôle sémantique,
> chaque instance désigne explicitement son repo, un registre de repos connus
> amortit la saisie.
>
> Scope **uniquement** la mécanique multi-repo. La métaphore de liste TUI
> (regroupement, arbre, onglets…) est **explicitement hors scope** : la liste
> reste plate, inchangée. Le design TUI viendra en dernier, dans un plan
> séparé, après usage réel.

---

## Décisions verrouillées (issues du grilling)

1. **Source des repos :** registre cs2 autonome, **permissif**. Le sélecteur
   de repo à la création propose les repos connus + accepte un path libre
   non enregistré. Tout path libre valide nourrit le registre pour la fois
   suivante.
2. **Cwd :** le garde `IsGitRepo(cwd)` est **retiré**. cs2 démarre depuis
   n'importe où ; cwd n'est plus que le répertoire de lancement.
3. **Branch-picker :** **refactor propre** — le picker reçoit `repoPath` en
   paramètre explicite, ne dérive plus le repo d'une instance à moitié
   construite ni de `os.Getwd()`.
4. **Flow de création :** repo choisi **avant** branche (le picker a besoin
   du repo pour scanner les branches).
5. **Persistance :** dossier config dédié `~/.cs2/`, **coupe le partage avec
   `cs` officiel**. Résout par la même occasion l'item différé « separate
   config dir ».
6. **Format du registre :** **minimal** — liste de paths nus
   (`repos: []string`). Pas d'alias, pas de défaut, pas de tri. Voir
   `roadmap_and_ideas.md` pour les enrichissements repoussés.
7. **Migration :** **démarrage à froid, zéro migration**. `~/.cs2/` démarre
   vide. Les instances existantes dans `~/.claude-squad/` sont ignorées par
   cs2.
8. **Limite d'instances :** **retirée**. Le garde `GlobalInstanceLimit = 10`
   disparaît. Le superviseur ne bloque pas arbitrairement.
9. **Lancement `cs2` :** **inchangé**. Charge toutes les instances persistées
   (depuis `~/.cs2/`), affiche la liste plate. Le sélecteur de repo
   n'intervient qu'à la création.

---

## État des lieux (les points de couplage à supprimer)

1. `main.go:48` — `if !git.IsGitRepo(currentDir)` refuse le démarrage hors
   repo git. (Décision 2 : retirer.)
2. `app/app.go:24` — `const GlobalInstanceLimit = 10` + checks
   `app/app.go:613,642`. (Décision 8 : retirer.)
3. `app/app.go:627,648` — `NewInstance(...Path: "."...)` hardcoded. Jamais
   de sélecteur de repo. (Décision 1, 4 : remplacer par sélecteur.)
4. `app/app.go:620-621,893-894` — `FetchBranches(os.Getwd())` /
   `SearchBranches(os.Getwd(), ...)` : le repo scanné est cwd, pas le repo
   de l'instance. Bug latent dès que plusieurs repos cohabitent. (Décision 3 :
   refactor.)
5. `config/config.go:18` — `GetConfigDir()` retourne `~/.claude-squad`,
   partagé avec `cs`. (Décision 5 : `~/.cs2/`.)

Le modèle de données est **déjà compatible** : `InstanceData.Worktree.RepoPath`
est persisté (`session/storage.go:13,29`). C'est la création + la config qui
ignorent le multi-repo, pas le stockage.

---

## Étapes (commits atomiques, chacun vert + testé)

### Étape 1 — Scission du dossier config (`~/.cs2/`)

**Décision couverte :** 5, 7.

**Fichiers :**
- `config/config.go` — `GetConfigDir()` retourne `~/.cs2/` au lieu de
  `~/.claude-squad/`. Crée le dossier s'il n'existe pas.
- `config/config_test.go` — test que `GetConfigDir()` pointe sous le home,
  suffixe `.cs2`, et est créée si absente.

**Pas de migration.** Au premier lancement, `~/.cs2/` est vide : pas de
`config.json`, pas de `state.json`, pas d'instances. `LoadConfig` retourne
une config par défaut (comme un `cs2 reset` implicite, mais silencieux).

**Commit :** `feat(config): use dedicated ~/.cs2 config dir, decouple from cs`

Coupure propre avec le `cs` officiel. `cs2 debug` affichera le nouveau chemin.

---

### Étape 2 — Couche de stockage du registre de repos

**Décision couverte :** 1 (moitié stockage), 6.

**Fichiers (nouveau package) :**
- `repo/registry.go` — type `Registry`, méthodes `List()`, `Add(path string)`,
  `Remove(path string)`, `Contains(path string)`. Persistance dans
  `~/.cs2/repos.json` sous forme `[]string`. `Add` déduplique et résout en
  chemin absolu.
- `repo/registry_test.go` — `Add` déduplique, `Remove` idempotent, `List`
  tri stable, persistance round-trip (save → load).

**Pas d'UI.** Le registre est une bibliothèque isolée, testable sans TUI.

**Commit :** `feat(repo): add repo registry storage (minimal path list)`

---

### Étape 3 — Retirer le garde `IsGitRepo(cwd)`

**Décision couverte :** 2.

**Fichiers :**
- `main.go` — supprimer le bloc `if !git.IsGitRepo(currentDir) { ... }`
  (lignes ~43-50). Supprimer l'import `git` devenu inutilisé si c'est le seul
  usage dans `main.go`.

**Tests :**
- Pas de test dédié ; la vérification est que `cs2` démarre depuis un
  répertoire non-git sans erreur (testé manuellement + le build reste vert).
  On peut ajouter un test d'intégration léger plus tard si besoin.

**Commit :** `refactor(main): drop IsGitRepo cwd guard for multi-repo`

---

### Étape 4 — Retirer la limite d'instances

**Décision couverte :** 8.

**Fichiers :**
- `app/app.go` — supprimer `const GlobalInstanceLimit = 10` (ligne 24) et les
  deux blocs `if m.list.NumInstances() >= GlobalInstanceLimit { ... }`
  (lignes ~613-616 et ~642-645).

**Tests :**
- Existant ; vérifier que `go test ./app/...` reste vert (les tests ne
  dépendent pas de la limite).

**Commit :** `refactor(app): remove global instance limit`

---

### Étape 5 — Refactor du branch-picker : `repoPath` explicite

**Décision couverte :** 3.

C'est le commit le plus délicat (signatures multiples). On le scinde en
sous-étapes pour rester atomique.

**Fichiers concernés :**
- `app/app.go` — `scheduleBranchSearch` / `runBranchSearch` (lignes ~882-901)
  utilisent `os.Getwd()`. Les passer en paramètre `repoPath string`.
- `app/app.go` — les appelants du picker : `KeyPrompt` (~620) et `KeyNew`
  (~648) passent le `repoPath` actuel. **Pendant la transition**, ce `repoPath`
  est dérivé de l'instance en cours de création (`instance.Path` résolu en
  absolu). C'est correct mais temporaire — l'Étape 6 remplacera cette source
  par le sélecteur de repo.

**Sous-étapes :**
- 5a. Ajouter le paramètre `repoPath` aux fonctions de recherche de branches
  et le propager. Source transitoire : `instance.Path`. Build + tests verts.
- 5b. Supprimer l'appel à `os.Getwd()` dans le chemin du picker. Vérifier
  qu'aucun autre appelant ne dépend de l'ancien comportement.

**Tests :**
- `app/` — si un test couvre le picker, le mettre à jour pour passer un
  `repoPath` explicite. Sinon, ajouter un test que `runBranchSearch` avec un
  `repoPath` de repo de test retourne bien les branches de *ce* repo (pas de
  cwd).

**Commit :** `refactor(app): pass repoPath explicitly to branch picker`

Après ce commit, le bug latent (point 4) est corrigé structurellement : le
picker ne lit plus jamais cwd.

---

### Étape 6 — Sélecteur de repo à la création (registre + path libre)

**Décisions couvertes :** 1 (moitié UI), 4 (repo avant branche).

**Fichiers :**
- `app/app.go` — dans les handlers `KeyPrompt` / `KeyNew`, avant
  `NewInstance`, insérer une étape de sélection de repo :
  - Proposer la liste du registre (`repo.Registry.List()`).
  - Offrir une option « path libre » (saisie texte).
  - Valider le path choisi via `git.IsGitRepo` (erreur claire si invalide).
  - Si le path était saisi librement **et** est valide, l'ajouter au
    registre (`Registry.Add`) pour la fois suivante.
- `session/instance.go` / `app/app.go` — `NewInstance` reçoit le `repoPath`
  résolu (plus `Path: "."`). Le `Path` de l'instance devient le repo choisi.
- Branchage : le `repoPath` sélectionné est passé au picker de l'Étape 5.

**UI :** minimale. Un overlay/liste bubbletea simple pour choisir parmi le
registre + champ libre. **Pas de design** — fonctionnel uniquement. Le
rendu viendra avec le futur plan TUI.

**Tests :**
- `repo/` — déjà couvert (Étape 2).
- `app/` — test que la création avec un path libre valide ajoute au registre.
- Test que la création avec un path invalide renvoie une erreur sans créer
  d'instance.

**Commit :** `feat(app): add repo selector at instance creation (registry + free path)`

C'est le commit qui **active** le multi-repo : à partir d'ici, chaque instance
peut pointer vers un repo différent, choisi explicitement.

---

### Étape 7 — Branch-picker : brancher le `repoPath` du sélecteur

**Décisions couvertes :** 4 (wiring final).

**Fichiers :**
- `app/app.go` — remplacer la source transitoire de l'Étape 5
  (`instance.Path`) par le `repoPath` retourné par le sélecteur de l'Étape 6.
  Le flow devient : sélection repo → `NewInstance(repoPath)` → picker de
  branche sur ce `repoPath`.

**Tests :**
- Vérifier bout-en-bout (test d'intégration léger) : création d'une instance
  dans un repo A, le picker de branche scanne bien A et pas cwd.

**Commit :** `refactor(app): wire repo selector output to branch picker`

Après ce commit, le flow de création est cohérent de bout en bout : repo
choisi explicitement → branches de ce repo → instance dans ce repo.

---

## Critères de succès (vérifiables)

1. `go build ./...` et `go test ./...` passent après chaque commit.
2. `cs2` démarre depuis n'importe quel répertoire (pas seulement un repo git).
3. `cs2` utilise `~/.cs2/` (vérifiable via `cs2 debug`) et ne touche plus
   `~/.claude-squad/`.
4. À la création d'une instance, un sélecteur de repo propose les repos
   enregistrés + un path libre.
5. Un path libre valide est ajouté au registre et réapparaît au prochain
   lancement.
6. Le branch-picker scanne le repo de l'instance en cours de création,
   jamais cwd (vérifié par test).
7. Il n'existe plus aucune référence à `GlobalInstanceLimit` dans le code.
8. Il n'existe plus de garde `IsGitRepo(cwd)` au démarrage de `main.go`.
9. Une instance créée dans le repo A et une dans le repo B coexistent dans
   la liste plate, chacune avec son `RepoPath` persisté.

---

## Ce qui est **explicitement hors scope** de ce plan

- **Métaphore de liste multi-repo** (regroupement, arbre, onglets-filtre,
  colonnes). La liste reste plate. Décision produit repoussée à un plan
  ultérieur, après usage réel. Voir le skill `design-an-interface` quand le
  moment viendra.
- **Design TUI / rendu du sélecteur de repo.** Fonctionnel uniquement ici.
- **Import one-shot des repos depuis VS Code.** Voir `roadmap_and_ideas.md`.
- **Enrichissement du registre** (alias, repo par défaut, tri par récence).
  Voir `roadmap_and_ideas.md`.
- **Migration des instances existantes** depuis `~/.claude-squad/`. Décision
  explicite : démarrage à froid.
- **Détection d'état Pi au-delà du sentinel** (trust prompt, etc.). Hors
  scope, déjà noté dans le handoff.
