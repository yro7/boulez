# Refactor Plan — App architecture & project hygiene

RFC pour refactor incrémental de `app/app.go` et nettoyage de la dette
identifiée pendant la review. Chaque commit laisse le codebase en état de
build + tests verts (principe Fowler : chaque étape aussi petite que possible).

---

## Problem Statement

`app/app.go` est un monolithe de 1850 lignes (38 méthodes) hérité du `home`
model de claude-squad. La struct `home` porte ~30 champs couvrant 4
responsabilités (config, état d'UI transitoire, stores persistants, seams
kernel), ce qui viole SRP par construction. Le point de douleur concret est
`handleKeyPress` (478 lignes) : un dispatch d'état partiellement extrait
(`handleHelpState` vit dans `help.go`, mais les handlers host/repo/preset/new/
prompt sont restés inlinés). Un commentaire ligne 155 référence
`handlePromptState` qui **n'existe pas** — l'extraction a été annoncée puis
abandonnée.

Dette annexe identifiée pendant la review :
- `startingInstance *session.Instance` : champ mort, explicitement « Phase 4
  removes it », 0 référence en production ni en test.
- Doublon `app.SpawnOptions` / `kernel.SpawnOptions` : structures parallèles
  tenues à la main, seule entorse DRY du projet, risquée de drift silencieux.
- `gofmt` signale `main.go` (tri d'imports).
- `web/` (landing Next.js héritée) et `CONTRIBUTING.md` (template minimal) —
  déjà supprimés dans un commit séparé, non couverts par ce plan.

## Solution

Refactor incrémental en 4 phases, de la plus sûre à la plus structurante.
Chaque phase est indépendante et shippable seule. L'objectif n'est **pas** de
casser le `home` model (inhérent à Bubble Tea) mais de :

1. Honorer le pattern d'extraction déjà établi (`help.go`, `spawn.go`,
   `fleet_client.go`) pour les handlers d'état restants.
2. Supprimer la dette explicite nommée dans le code.
3. Réduire le risque de drift sur le doublon `SpawnOptions`.
4. Scinder les responsabilités de `home` en collaborateurs injectés, de façon
   optionnelle et différée.

## Commits

### Phase 0 — Hygiène (trivial, indépendant)

**Commit 0.1 — `chore: gofmt main.go`**
Trier les imports `cli` / `cmd2` dans `main.go` pour satisfaire `gofmt -l`.
Aucun changement de comportement.

**Commit 0.2 — `refactor(app): drop dead startingInstance field`**
Supprimer le champ `startingInstance *session.Instance` et son commentaire de
dette. Vérifié : 0 référence en production, 0 en test. Build + `go test ./...`
verts.

### Phase 1 — Extraction des handlers d'état de `app.go`

Le pattern est déjà établi : `handleHelpState` vit dans `help.go`. On applique
le même mouvement aux 5 handlers restants. Chaque commit = un fichier nouveau +
suppression du bloc inliné dans `app.go` + build/tests verts. L'ordre va du
plus isolé au plus couplé.

**Commit 1.1 — `refactor(app): extract handlePromptState to prompt_state.go`**
Extraire le bloc `else if m.state == statePrompt` (~90 lignes, lignes
~632–720) en une méthode `handlePromptState(msg tea.KeyMsg) (tea.Model,
tea.Cmd)` dans un nouveau fichier `app/prompt_state.go`. Honore le commentaire
fantôme ligne 155 qui référençait cette méthode inexistante. Le comportement
est couvert par `TestPromptOverlayPreselectsProfileFromPreference` et les
tests de branch search.

**Commit 1.2 — `refactor(app): extract handleNewState to new_state.go`**
Extraire le bloc `if m.state == stateNew` (~100 lignes, lignes ~533–631) en
`handleNewState`. Couvre la saisie du titre d'instance (entrées clavier,
backspace, enter, esc, ctrl+c). Couvert par les tests de repo/host select qui
atteignent l'état `stateNew`.

**Commit 1.3 — `refactor(app): extract handleHostSelectState to host_select.go`**
Déplacer `handleHostSelectState` (~60 lignes) + `openHostSelector` (~30
lignes) + `applyHostDeletions` de `app.go` vers `app/host_select.go`. Couvert
par `TestHostSelectSkippedWhenRegistryEmpty`,
`TestHostSelectSkippedWhenSingleAlias`, `TestHostSelectOpensWhenTwoAliases`.

**Commit 1.4 — `refactor(app): extract handleRepoSelectState to repo_select.go`**
Déplacer `handleRepoSelectState` + `openRepoSelector` + `filterRepos` +
`applyRepoDeletions` vers `app/repo_select.go`. Couvert par
`TestRepoSelectFreePathValidAddsToRegistryAndCreatesInstance`,
`TestRepoSelectInvalidPathShowsErrorAndCreatesNoInstance`,
`TestRepoSelectKnownRepoCreatesInstanceWithoutMutatingRegistry`,
`TestRepoSelectCancelReturnsToDefault`.

**Commit 1.5 — `refactor(app): extract handlePresetSelectState to preset_select.go`**
Déplacer `handlePresetSelectState` + `openPresetSelector` +
`startNewInstanceFromPreset` vers `app/preset_select.go`. Couvert par
`app/preset_test.go`.

**Commit 1.6 — `refactor(app): extract View to view.go`**
Déplacer `View()` (49 lignes) + `pinOrchestratorsFirst` vers `app/view.go`.
Pure présentation, sans logique d'état. Trivial.

**État après Phase 1** : `app.go` tombe à ~1100 lignes (struct + `Run` +
`newHome` + `Update` + le switch `stateDefault` des keys). `handleKeyPress`
passe de 478 à ~250 lignes (dispatch d'état + switch des keys default).

### Phase 2 — Réduire le risque de drift `SpawnOptions`

`app.SpawnOptions` et `kernel.SpawnOptions` ont des champs parallèles. La
conversion se fait champ-par-champ dans `kernel/transport.go` (`spawnParams.toOptions`)
et à la construction dans `app/app.go`. Tout renommage d'un côté sans l'autre
= bug silencieux.

**Commit 2.1 — `test(kernel): add SpawnOptions parity test`**
Ajouter un test qui vérifie que les deux structs ont exactement les mêmes
champs (noms + types) via reflection. Ce test échouera bruyamment à la prochaine
divergence. Pas de changement de production.

**Commit 2.2 — `refactor(kernel): centralize SpawnOptions conversion`**
Ajouter `kernel.SpawnOptionsFromApp(app.SpawnOptions) kernel.SpawnOptions` (ou
un mapper équivalent) et l'utiliser partout où la conversion se fait à la main
(`transport.go` + `app/fleet_client.go` côté reverse si applicable). Un seul
endroit tient la correspondance. Le test de parité 2.1 reste en sentinel.

### Phase 3 — Scinder les responsabilités de `home` (optionnel, différé)

`home` porte 4 catégories de champs. La phase 1 traite l'UI transitoire. La
phase 3 attaque les stores persistants et les seams, à condition que la phase 1
soit stable. **Cette phase est marquée out-of-scope par défaut** — à ne lancer
que si un besoin concret le justise (ex. testabilité des stores sans le TUI).

**Commit 3.1 — `refactor(app): extract persistent stores into appStores`**
Regrouper `repoRegistry`, `hostRegistry`, `prefs`, `presetStore` dans un type
`appStores` injecté dans `home`. `newHome` construit l'`appStores`, les tests
peuvent l'injecter. Découple la persistance du modèle d'UI.

**Commit 3.2 — `refactor(app): extract overlay controller`**
Les 5 overlays (`textInputOverlay`, `textOverlay`, `confirmationOverlay`,
`repoSelector`, `hostSelector`, `presetSelector`) + leur logique de cycle de
vie dans un `overlayController`. Réduit encore `handleKeyPress`.

## Decision Document

- **Stratégie** : extraction mécanique, préserver le type receiver `*home` et
  la signature `tea.Msg` du Bubble Tea. Aucun changement d'API publique.
- **Ordre des extractions** : du plus isolé (`prompt_state`, déjà commenté) au
  plus couplé. Chaque handler reste une méthode sur `*home` — on déplace du
  code, on ne change pas l'architecture.
- **`handlePromptState` d'abord** parce qu'il honore un commentaire qui ment
  actuellement sur l'état du code (le plus petit commit qui corrige une
  incohérence documentée).
- **`SpawnOptions`** : on ne fusionne pas les deux structs (la séparation de
  couche app/kernel est justifiée par le commentaire du kernel). On ajoute un
  test de parité + un convertisseur centralisé pour rendre le drift
  détectable plutôt que de l'éliminer par fusion.
- **`startingInstance`** : suppression pure, aucune migration (0 référence).
- **Phase 3** : laissée optionnelle. Le `home` model reste acceptable tant que
  `handleKeyPress` est sous ~250 lignes. On ne scinde les stores que si un
  besoin de testabilité le justifie — sinon c'est de l'over-engineering
  (« one adapter means a hypothetical seam; two means a real one » appliqué à
  l'intérieur du paquet app).
- **Pas de renommage** de fichiers existants : on ajoute, on ne déplace pas.
  `app.go` reste le foyer de la struct + `Update`/`View`/`Run`.

## Testing Decisions

- **Build + `go test ./...` verts après chaque commit** — c'est le filet de
  sécurité. Les tests existants couvrent déjà les overlays à extraire.
- **Pas de nouveaux tests fonctionnels** pour la phase 1 : on déplace du code,
  le comportement externe ne change pas, les tests existants suffisent.
- **Test de parité `SpawnOptions`** (commit 2.1) : test d'invariant par
  reflection, pas de comportement. Prior art : `program/registry_test.go`
  qui valide la registry par des assertions structurelles.
- **Critère d'un bon test ici** : il doit échouer si quelqu'un casse
  l'invariant extrait (parité de champs, comportement d'un handler). Pas de
  test sur les détails d'implémentation de l'extraction elle-même.
- **`go vet ./...`** comme garde-fou supplémentaire à chaque commit.

## Out of Scope

- **Réécriture du `home` model** : Bubble Tea veut un `Model` racine unique.
  On ne remplace pas `home`, on l'amincit.
- **Refonte visuelle / UX** : AGENTS.md dit « Design / UX comes last ». Aucun
  changement de rendu, de keybinding, ou de wording.
- **Refactor du kernel / daemon / program** : ces paquets sont sains. Seul
  `app/` et le doublon `SpawnOptions` sont touchés.
- **Phase 3** (`appStores`, `overlayController`) : différée, à ne lancer que
  sur besoin concret. Incluse ici comme feuille de route, pas comme
  engagement.
- **Extraction du switch `stateDefault`** (les 19 `case keys.*`) : ce switch
  est homogène et lisible, pas un point de douleur. On le laisse dans
  `app.go`.
- **Tests d'intégration tmux** : hors scope, la testabilité sans tmux est déjà
  assurée par le seam `Spawner` du kernel.
- **Filer ce plan en issue GitHub** : non fait automatiquement ; à demander
  si voulu.

## Further Notes

- L'historique git montre que l'auteur fait déjà ce refactor incrémental
  (commit `263f65b` « move cobra command glue out of package main into
  cli/ », `help.go` / `spawn.go` / `fleet_client.go` déjà extraits). Ce plan
  poursuit un mouvement déjà engagé, il ne l'invente pas.
- La phase 1 est faible risque car purement mécanique (move de méthodes sur
  `*home` vers d'autres fichiers du même paquet). Aucune signature ne change.
- Après phase 1 + 2, `app.go` sera à ~1100 lignes et le projet n'aura plus de
  dette nommée explicitement dans le code (`startingInstance` supprimé,
  commentaire `handlePromptState` honoré, doublon `SpawnOptions` sentinellé).
  C'est l'objectif minimal satisfaisant.
