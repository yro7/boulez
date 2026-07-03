# Plan : amélioration de l'UX à la création d'instance

> **Statut : implémenté.** Les 6 étapes sont commitées (voir `git log`),
> build + `go test ./...` verts. Les critères de succès ci-dessous sont
> tous vérifiés.
>
> Objectif : raccourcir et lisser le flow de création d'instance
> (host → repo → nom → prompt/branche) quand on refait souvent la même chose,
> et nettoyer les registres depuis le TUI.

## Décisions verrouillées (issues de la discussion)

1. **Filtre fzf fusionné.** Taper dans le sélecteur host/repo filtre la liste
   en live **et** peut créer une entrée libre : si un item matche, Enter le
   sélectionne ; si rien ne matche, Enter crée un path/alias libre. Pas
   d'onglet/tab de filtre séparé — un seul geste. Valide pour host et repo.
2. **Suppression silencieuse (ctrl+d).** `ctrl+d` sur une entrée connue la
   retire du registre sans confirmation overlay. Pas d'undo explicite (le path
   peut être re-saisi librement la fois suivante). `local` n'est jamais
   supprimable (implicite, hors registre).
3. **Préférence repo→profil explicite.** L'utilisateur set explicitement la
   préférence (pas d'auto-apprentissage au début). Au moment de construire le
   prompt overlay, si une préférence existe pour le repo sélectionné, le
   `ProfilePicker` pré-sélectionne ce profil. L'utilisateur peut encore changer.
4. **Saut de l'étape host quand triviale.** Si le registre host est vide
   (zéro alias ssh), `local` est la seule option : on saute le sélecteur et on
   va direct au repo. Un seul alias enregistré saute aussi (on prend cet
   alias). Le sélecteur ne s'ouvre que s'il y a un vrai choix (≥2 alias, ou
   ≥1 alias + local = ≥2 options).

## Ce qui est **explicitement hors scope** de ce plan

- **Auto-apprentissage** de la préférence repo→profil. Voir `roadmap_and_ideas.md`
  (idée repoussée #1). Le plan pose la préférence explicite ; l'auto-learn sera
  une bascule au-dessus une fois stabilisée.
- **Nom d'instance auto-suggéré**, **branche courante pré-sélectionnée**,
  **raccourci « new here »**, **mémo du dernier prompt**. Idées repoussées
  dans `roadmap_and_ideas.md`, à réévaluer après usage réel de ce plan.
- **Design TUI / rendu.** Fonctionnel uniquement, comme le plan multi-repo.
- **Registre de repos per-host.** Toujours déféré (dette SSH v2, point 2).

## État des lieux

- `ui/overlay/hostSelector.go` et `ui/overlay/repoSelector.go` sont du code
  **miroir** : liste + une ligne free-text en dernière position, `move`,
  `HandleKeyPress`, `Render`. Aucun filtre live, pas de suppression.
- Les registres `host.Registry` / `repo.Registry` ont déjà `List/Add/Remove/
  Contains`. `Remove` est déjà câblé nulle part dans l'UI.
- `openHostSelector` (`app/app.go:1157`) ouvre toujours le sélecteur, même si
  le registre est vide (un seul `local`).
- `newPromptOverlay` (`app/app.go:1107`) construit l'overlay sans connaître
  le repo : pas de pré-sélection de profil possible aujourd'hui.
- `ProfilePicker` garde un `cursor` interne ; pré-sélectionner = setter le
  cursor à l'index du profil préféré (méthode à ajouter).

---

## Étapes (commits atomiques, chacun vert + testé)

### Étape 1 — Extraire `ListSelector` (module profond commun)

**Décision couverte :** préparation de 1, 2, 4 (MRU).

`HostSelector` et `RepoSelector` sont déjà dupliqués ; ajouter filtre + delete +
MRU sur les deux sans extraire ferait une 3e copie. On factorise d'abord.

**Fichiers (nouveau fichier dans `ui/overlay/`) :**
- `listSelector.go` — type `ListSelector` paramétrable :
  - champs : `items []string` (libellés affichés), `values []string` (valeur
    renvoyée à la sélection, peut == libellé), `cursor`, `filter string`,
    `allowFree bool`, `freeLabel string` (préfixe d'affichage, ex. `"Path: "` /
    `"Alias: "`), `width`, `Submitted/Canceled`, `deletedItems []string`
    (entrées supprimées ce tour, pour que l'appelant appelle `Registry.Remove`).
  - méthode `FilteredItems() []string` : items dont le libellé matche
    `filter` (sous-chaîne, insensible à la casse).
  - `HandleKeyPress` : ↑↓ déplacent le curseur **parmi les items filtrés** ;
    runes/backspace éditent `filter` ; Enter soumet (item filtré sous le
    curseur, ou free-text si `allowFree` et curseur sur la ligne libre, ou
    si aucun item ne matche et `allowFree` → le texte du filtre devient la
    valeur libre) ; `ctrl+d` supprime l'item sous le curseur de `items` et
    l'ajoute à `deletedItems` (pas de close) ; Esc annule.
  - `SelectedValue()`, `IsFreeValue()`, `DeletedValues()`, `Render()`.
  - Le rendu réutilise les styles existants (`rsStyle` etc.).
- `hostSelector.go` / `repoSelector.go` — deviennent des **wrappers minces**
  qui construisent un `ListSelector` (local + aliases pour host ; repos pour
  repo) et délèguent. Préservent l'API publique existante
  (`NewHostSelector`, `SelectedAlias`, `IsFreeAlias`, etc.) pour ne pas
  toucher `app/app.go` dans ce commit.

**Tests :**
- `listSelector_test.go` — filtre réduit la liste ; Enter sur item filtré
  renvoie sa valeur ; Enter quand filtre ne matche rien + `allowFree` renvoie
  le texte tapé comme valeur libre ; `ctrl+d` retire l'item et apparaît dans
  `DeletedValues()` ; `local` (item non supprimable via flag) ignore `ctrl+d`.
- `hostSelector_test.go` / `repoSelector_test.go` — existants passent à
  l'identique via les wrappers (behaviour conservé, sans filtre encore).

**Commit :** `refactor(overlay): extract ListSelector from host/repo selectors`

Après ce commit : un seul module porte la logique de liste. Les étapes
suivantes ne touchent qu'`ListSelector` + le câblage dans `app/app.go`.

---

### Étape 2 — Filtre fzf fusionné (live + free-text)

**Décision couverte :** 1.

**Fichiers :**
- `ui/overlay/listSelector.go` — le filtre est déjà éditable à l'étape 1 ;
  ici on finalise la sémantique **fusionnée** : la ligne free-text **est** le
  filtre. Plus de ligne séparée « Path: ... » en bas. Comportement Enter :
  1. si un item filtré existe et le curseur est dessus → sélectionne cet item ;
  2. sinon si `allowFree` et `filter != ""` → `filter` devient la valeur libre ;
  3. sinon → erreur (rien à sélectionner), l'appelant réaffiche.
  Le curseur par défaut est sur le premier item filtré (ou la zone libre si
  vide), pour qu'un Enter immédiat sélectionne le top du filtre.
- `app/app.go` — `handleRepoSelectState` / `handleHostSelectState` : la
  valeur libre est maintenant obtenue via `IsFreeValue()` (le filtre lui-même),
  la logique d'enregistrement `Registry.Add` est inchangée (déjà conditionnée
  à `IsFreePath`/`IsFreeAlias`).
- Ajuster les hints rendus : « type to filter · enter select · ctrl+d delete ·
  esc cancel ».

**Tests :**
- `listSelector_test.go` — cas 1/2/3 ci-dessus ; un filtre qui matche un seul
  item + Enter sélectionne cet item (pas le free-text) ; un filtre vide + Enter
  sélectionne le premier item.

**Commit :** `feat(overlay): fzf-style filter fused with free-text in selectors`

---

### Étape 3 — Suppression silencieuse (ctrl+d)

**Décision couverte :** 2.

`ctrl+d` est déjà géré dans `ListSelector` (étape 1) au niveau UI. Ici on
câble l'appel au registre.

**Fichiers :**
- `app/app.go` — dans `handleRepoSelectState` (et `handleHostSelectState`),
  après `HandleKeyPress`, itérer sur `DeletedValues()` et appeler
  `repoRegistry.Remove(v)` / `hostRegistry.Remove(v)` (best-effort, comme
  `Add` déjà fait). Ne pas fermer l'overlay : l'utilisateur peut enchaîner
  plusieurs suppressions. `local` est exclu par le flag non-supprimable du
  `ListSelector` (étape 1).
- `ui/overlay/listSelector.go` — s'assurer que supprimer l'item sous le
  curseur recale le curseur sur un item valide et rafraîchit le filtre.

**Tests :**
- `app/` (si un test couvre le flow de création) ou test d'intégration léger :
  ctrl+d sur un repo connu retire l'entrée du registre (vérifier via
  `registry.Contains`). Au minimum, test unitaire sur `ListSelector` que
  `DeletedValues` reflète les suppressions et que `local` est protégé.

**Commit :** `feat(app): silent ctrl+d removal of hosts/repos from registries`

---

### Étape 4 — MRU : l'entrée sélectionnée remonte en tête

**Décision couverte :** 4 (MRU).

Le roadmap diffère le tri par récence « selon volume réel » ; l'usage est
confirmé (cs2 revient souvent). On l'active, **sans changer le format de
stockage** : le tri se fait par réordonnancement de la liste sauvegardée.

**Fichiers :**
- `repo/registry.go` — ajouter `Touch(path string) error` : résout en absolu,
  retire l'entrée de sa position et la réinsère **en tête** (ordre = du plus
  récemment utilisé au plus ancien). No-op si absent. Préserve `List` comme
  source de vérité (l'ordre devient MRU, insertion ordre rompu sciemment).
  Mettre à jour le commentaire de `List` : « ordre MRU (plus récent en tête) ».
- `host/registry.go` — même `Touch(alias)`.
- `repo/registry_test.go` / `host/registry_test.go` — `Touch` déplace en tête,
  idempotent, préserve les autres entrées, no-op sur absent. Le test
  d'ordre existant (si « insertion order ») est mis à jour pour MRU.
- `app/app.go` — appeler `Touch` sur le path/alias **sélectionné** à la soumission
  (connu **ou** fraîchement ajouté : pour un free-path, `Add` puis `Touch`).
  Câbler dans `handleHostSelectState` / `handleRepoSelectState` à côté des
  `Add`/`IsFree*` existants.

**Tests :**
- Vérifier qu'après sélection d'un repo connu, il remonte en tête du registre
  au prochain `openRepoSelector`.

**Commit :** `feat(registry): MRU ordering via Touch on selection`

---

### Étape 5 — Sauter l'étape host quand triviale

**Décision couverte :** 3.

**Fichiers :**
- `app/app.go` — `openHostSelector(promptFlow)` : compter les alias du
  registre. Si `len(aliases) == 0` → `pendingHost = host.Local`, skip direct
  à `openRepoSelector(promptFlow)`. Si `len(aliases) == 1` → prendre cet
  alias (`host.Lookup(aliases[0])`), skip aussi. Sinon ouvrir le sélecteur
  comme aujourd'hui. Le comptage est local et instantané (pas de probe ssh).
- Préserver le chemin explicite : si l'utilisateur veut *forcer* le sélecteur
  (ex. pour ajouter un 2e alias), il passe par le flow normal qui, une fois un
  alias saisi librement, est enregistré — au prochain lancement le sélecteur
  s'ouvrira (≥2 options). Pas de touche « forcer sélecteur » dans ce plan.

**Tests :**
- `app/` — test que `openHostSelector` avec un registre vide ne crée pas de
  `hostSelector` et positionne `pendingHost = Local` ; test qu'avec un alias,
  `pendingHost` pointe vers cet alias sans ouvrir l'overlay.

**Commit :** `feat(app): skip host selector when registry has <2 aliases`

---

### Étape 6 — Préférence repo→profil (explicite)

**Décision couverte :** 3 (préférence).

**Nouveau package `prefs`** (SRP : connaît les préférences repo, rien d'autre).
Format minimal `~/.cs2/preferences.json` : `{ "<absRepoPath>": {"profile": "<name>", "program": "<program>"} }`.

**Fichiers :**
- `prefs/prefs.go` — `Store` avec `Get(repoPath) (Profile, bool)` et
  `Set(repoPath, profileName, program string)`, `Clear(repoPath)`. Persistance
  JSON, résolution absolue du repoPath, self-heal sur fichier corrompu.
  Modèle `roadmap` : format minimal, migration triviale plus tard.
- `prefs/prefs_test.go` — round-trip, Get/Set/Clear, abs path résolu.
- `config/config.go` — exposer un type `ProfilePref { Name, Program }` ou
  réutiliser `config.Profile`. Garder `prefs` indépendant de `config` (pas
  de couplage ; `prefs` définit son propre petit struct pour rester SRP).
- `ui/overlay/profilePicker.go` — ajouter `SetSelectedByName(name string)` :
  positionne le cursor sur le profil de ce nom (no-op si absent). Préserve
  `GetSelectedProfile`.
- `ui/overlay/textInput.go` — `NewTextInputOverlayWithBranchPicker` accepte
  une préférence de profil optionnelle (nouveau constructeur ou param) et
  l'applique au `ProfilePicker` créé.
- `app/app.go` :
  - `newPromptOverlay()` → `newPromptOverlay(repoPath)` : regarde
    `m.prefs.Get(repoPath)` ; si présent, pré-sélectionne le profil dans
    l'overlay. Tous les appelants (`stateNew` Enter, `instanceStartedMsg`
    promptAfterName) passent le `instance.Path`.
  - **Set explicite :** sur le `ProfilePicker` focusé, touche `ctrl+s` appelle
    `prefs.Set(repoPath, profilCourant)` ; un hint court l'indique. Pas de menu
    séparé. (Discoverable via le hint du picker quand focus.)
  - Charger `m.prefs` au démarrage (`prefs.New()`), à côté des autres
    registries.

**Tests :**
- `prefs/` — couvert.
- `app/` — test que `newPromptOverlay` avec une préférence existante pré-
  sélectionne le bon profil (vérifier `GetSelectedProgram` avant interaction).
- `app/` — test que `ctrl+s` sur le profile picker persiste la préférence
  pour le repo courant.

**Commit :** `feat(prefs): explicit repo→profile preference, preselected at prompt`

---

## Critères de succès (vérifiables)

1. `go build ./...` et `go test ./...` verts après chaque commit.
2. Taper dans le sélecteur host/repo filtre la liste en live ; Enter sur un
   item filtré le sélectionne ; Enter avec un filtre ne matchant rien crée une
   entrée libre (host alias / repo path).
3. `ctrl+d` retire une entrée connue du registre, silencieusement, sans fermer
   le sélecteur ; `local` n'est jamais supprimable.
4. Sélectionner un repo/host le fait remonter en tête du registre au prochain
   lancement (MRU).
5. Au lancement de `cs2` avec un registre host vide, `KeyNew`/`KeyPrompt` va
   directement au sélecteur de repo (étape host sautée) ; idem avec un seul
   alias.
6. Après avoir `ctrl+s` sur le profile picker pour le repo `cs2`, relancer un
  `KeyPrompt` sur `cs2` pré-sélectionne ce profil dans le prompt overlay.
7. Aucune duplication de la logique de liste : `HostSelector` et `RepoSelector`
   délèguent à `ListSelector`.
8. Le format de stockage des registres reste `[]string` (pas de migration), seuls
   les commentaires d'ordre changent pour MRU.
