# Plan : « Quick session » — presets nommés via Ctrl+R

> **Statut : implémenté.** Build + `go test ./...` verts. Les commits sont
> atomiques par étape (voir `git log`).

Objectif : un raccourci `Ctrl+R` ouvre un picker de presets nommés. Un preset
est une **recette explicite complète** (host + repo + profil + prompt + branche)
qui skip les sélecteurs host/repo/prompt. Il ne reste que le nom de l'instance
à taper, puis l'instance démarre.

## Décisions verrouillées (issues de la discussion)

1. **Preset = recette explicite hardcodée.** Un preset duplique sciemment les
   valeurs que les registries/prefs connaissent déjà. C'est le point entier :
   reproductibilité totale, zéro indirection. Si tu veux la magie prefs
   (« utilise mon profil préféré pour ce repo »), tu utilises le flow `N`
   normal. Les deux coexistent, rôles différents.
2. **Read-on-open.** Le fichier `~/.cs2/presets.json` est relu frais à chaque
   ouverture du picker (pas de watcher fsnotify). Un agent ou éditeur peut le
   modifier entre deux Ctrl+R, cs2 prend le nouveau contenu au prochain coup.
3. **Voie TUI (Option 1), pas Spawn.** Ctrl+R → picker → submit → `stateNew`
   (name entry) avec instance pré-construite (host/repo/profile/branch déjà
   appliqués) → `Start`. `Spawn` reste réservé à l'orchestrator headless.
4. **Nom toujours demandé.** Le nom drive le nom de branche `cs2/<title>` et
   doit être unique ; un preset ne peut pas le fixer.
5. **Prompt auto-sent, pas d'overlay.** Un preset avec un prompt le stash sur
   l'instance (`instance.Prompt`) et l'envoie après `Start` via le handler
   `instanceStartedMsg` (le même chemin que le flow Shift+N). Pas d'overlay
   prompt — c'est le contrat « quick session ».
6. **Validation au submit.** Repo doit être un git repo (vérifié via
   `host.Executor`), profil doit exister dans `config.Profiles`. Sinon :
   erreur + retour au défaut, pas d'instance zombie.

## Ce qui est explicitement hors scope

- **Éditeur de preset dans le TUI.** Fichier uniquement. Si un UI plus tard,
  le format minimal se migrera trivialement (cf. `roadmap_and_ideas.md`).
- **Auto-suggestion** de preset (« tu utilises souvent celui-ci »).
- **Preset partagé entre hosts distants.** Un preset = un host fixe.
- **Watcher live pendant que l'overlay est ouvert.** Tu fermes/rouvres.

## Format du fichier

`~/.cs2/presets.json` :

```json
{
  "CS2 Work": {
    "repo": "/Users/me/cs2",
    "host": "local",
    "profile": "Pi",
    "prompt": "",
    "branch": ""
  }
}
```

Clé = nom affiché dans le picker. `host`/`profile`/`prompt`/`branch`
optionnels (défauts sains : `local` / program par défaut / vide / nouvelle
branche).

## Étapes (commits atomiques)

### Étape 1 — Package `presets/` (stockage)

**Décision couverte :** 1, 2, 5 (moitié stockage).

Miroir de `prefs/` : SRP, JSON map keyé par nom, self-heal (fichier corrompu
→ store vide, jamais d'erreur bloquante), read-on-open (relit le fichier à
chaque appel, pas de cache).

**Fichiers :**
- `presets/presets.go` — type `Preset{Repo, Host, Profile, Prompt, Branch}`,
  `Store` avec `List/Get/Set/Remove`. `List` tri alphabétique. `Set` résout
  le repo en absolu. `Remove` idempotent.
- `presets/presets_test.go` — round-trip, tri, self-heal corrompu, no-op
  Remove ne crée pas de fichier, read-on-open entre deux Store.

**Commit :** `feat(presets): named quick-session preset store`

### Étape 2 — `PresetSelector` overlay

**Décision couverte :** UX.

Réutilise `ListSelector` (le module extrait à l'étape 1 du plan UX). Pas de
free-text : un preset doit exister dans le fichier.

**Fichiers :**
- `ui/overlay/presetSelector.go` — wrapper mince sur `ListSelector`,
  `allowFree=false`, items non-supprimables (pas de ctrl+d).

**Commit :** `feat(overlay): add PresetSelector for quick sessions`

### Étape 3 — Keybinding `Ctrl+R` (`keys.KeyQuickSession`)

**Décision couverte :** shortcut.

**Fichiers :**
- `keys/keys.go` — `KeyQuickSession`, `GlobalKeyStringsMap["ctrl+r"]`,
  `GlobalkeyBindings` help `ctrl+r / quick session`.

**Commit :** `feat(keys): add Ctrl+R quick-session keybinding`

### Étape 4 — Câblage app + help + menu

**Décision couverte :** 3, 4, 6.

**Fichiers :**
- `app/app.go` :
  - champ `presetStore *presets.Store`, `presetSelector *overlay.PresetSelector`,
    état `statePresetSelect`.
  - `openPresetSelector()` : read-on-open, empty → erreur pointant le fichier.
  - `handlePresetSelectState()` : dispatch, cancel, submit → validation →
    `startNewInstanceFromPreset`.
  - `startNewInstanceFromPreset()` : résout host (`host.Lookup`), valide
    `git.IsGitRepo` via `host.Executor`, résout profil via
    `config.GetProfileByName`, stash prompt sur `instance.Prompt`, saute à
    `stateNew` (name entry). `promptAfterName=false` (auto-send).
  - resize + View pour `statePresetSelect`.
- `config/config.go` — `GetProfileByName(name) (string, bool)`.
- `app/help.go` — ligne `ctrl+r` dans l'aide générale.
- `ui/menu.go` — `KeyQuickSession` dans `defaultMenuOptions` (vide/instance
  sélectionnée). Suivi le pattern de `KeyPrompt` (pas dans `addInstanceOptions`
  pour préserver les groupes hardcoded).

**Tests :** `app/preset_test.go` — store vide → erreur, picker s'ouvre avec
presets, submit → stateNew + host/repo/profile appliqués, prompt stashé pour
auto-send, repo invalide rejeté, profil inconnu rejeté, cancel retour défaut,
branche appliquée.

**Commit :** `feat(app): Ctrl+R quick session from named presets`

## Critères de succès (vérifiables)

1. `go build ./...` et `go test ./...` verts.
2. `Ctrl+R` avec `~/.cs2/presets.json` vide → erreur pointant le fichier.
3. `Ctrl+R` avec presets → picker filtrable (fzf) ; Enter sur un preset →
   name entry direct, host/repo/profile/branch déjà appliqués.
4. Un preset avec un prompt l'envoie après Start (pas d'overlay prompt).
5. Un preset avec un repo non-git ou un profil inexistant est rejeté, aucune
   instance créée.
6. Le fichier est relu à chaque ouverture (hot-reload entre deux Ctrl+R).
7. Aucune nouvelle logique de liste : `PresetSelector` délègue à
   `ListSelector`.

## Exemple d'usage agent

Un agent peut dire : « crée un profil rapide "CS2 Work" avec Pi, prompt vide,
nouvelle branche, host local » → écrire dans `~/.cs2/presets.json` :

```json
{
  "CS2 Work": {
    "repo": "/Users/marin.decanini/cs-multirepo",
    "host": "local",
    "profile": "Pi",
    "prompt": "",
    "branch": ""
  }
}
```

Au prochain `Ctrl+R` dans le TUI, « CS2 Work » apparaît. Sélection → nom →
lancé.
