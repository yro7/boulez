# Plan : rendre les agents modulaires (détection d'état + prompts/permissions)

> Objectif : extraire le couplage agent-spécifique de `session/tmux/tmux.go`
> derrière un seam propre, pour qu'ajouter un nouvel agent (Pi, Codex, Amp…)
> se résume à **un fichier d'adapter** — zéro modification du cœur tmux.
>
> Scope **uniquement** la modularité agent. Le multi-repo et le design TUI
> viennent dans des plans ultérieurs. Aucune UI dans ce plan.

---

## État des lieux (le couplage à supprimer)

Aujourd'hui tout le savoir agent-spécifique vit en dur dans
`session/tmux/tmux.go`, sous forme de deux `if/else` sur `t.program` :

1. **Lignes 23–26** — constantes `ProgramClaude`/`ProgramAider`/`ProgramGemini`.
2. **`CheckAndHandleTrustPrompt()` (ligne 155)** — détecte et dismiss les
   prompts de *permission/trust* en scannant le contenu du pane tmux.
   ```go
   if strings.HasSuffix(t.program, ProgramClaude) {
       if strings.Contains(content, "Do you trust the files in this folder?") ||
          strings.Contains(content, "new MCP server") { t.TapEnter(); return true }
   } else {
       if strings.Contains(content, "Open documentation url for more info") {
           t.TapDAndEnter(); return true
       }
   }
   ```
3. **`HasUpdated()` (ligne 233)** — retourne `hasPrompt` (l'agent attend une
   saisie utilisateur = "ready") en scannant le contenu du pane.
   ```go
   if t.program == ProgramClaude          { hasPrompt = strings.Contains(content, "No, and tell Claude what to do differently") }
   else if strings.HasPrefix(t.program, ProgramAider) { hasPrompt = strings.Contains(content, "(Y)es/(N)o/(D)on't ask again") }
   else if strings.HasPrefix(t.program, ProgramGemini) { hasPrompt = strings.Contains(content, "Yes, allow once") }
   ```

**Problèmes :**
- Ajouter un agent = modifier `tmux.go` (le cœur d'exécution tmux), dans deux
  fonctions, avec des chaînes magiques. Aucune testabilité, aucun registre.
- Deux notions **mélangées** dans un seul booléen `hasPrompt` :
  (a) l'agent attend une saisie libre (état "ready"),
  (b) l'agent demande une permission/approbation. CS ne les distingue pas, donc
  l'auto-yes du daemon ne peut répondre qu'à ce qu'il croit être des prompts —
  d'où le comportement fragile actuel.
- Les trois agents gérés sont **codés en dur** ; un agent inconnu (Pi, Codex,
  Amp…) n'a ni détection d'état ni gestion de permission → badge "Ready"
  absent, auto-yes muet.

---

## Décisions de design (le seam)

Vocabulaire deep-module : aujourd'hui `HasUpdated`/`CheckAndHandleTrustPrompt`
sont **shallow** — un `if/else` inline. On les *deepen* en extrayant une
**interface `Adapter`** à un seam propre, avec un adapter concret par agent.

### Nouveau package `program/` (le seam)

Un fichier d'interface + un registre. Les adapters concrets vivent chacun
dans leur propre fichier. Le cœur tmux ne connaît **que l'interface**.

```go
// program/adapter.go
package program

// Responder est la surface minimale dont un adapter a besoin pour agir sur
// la session tmux sous-jacente (envoyer des touches). Volontairement étroite
// pour ne pas coupler les adapters à *TmuxSession et rester testable.
type Responder interface {
    TapEnter() error          // 0x0D
    TapDAndEnter() error      // 'D' + 0x0D
    SendKeys(keys string) error
}

// Status est l'état perçu de l'agent à partir du contenu du pane.
type Status int

const (
    StatusUnknown   Status = iota
    StatusWorking          // l'agent produit / tourne
    StatusReady            // l'agent attend une saisie utilisateur libre
    StatusPermission       // l'agent demande une permission/approbation
)

// Prompt décrit un prompt détecté dans le pane, avec une action de résolution
// optionnelle (auto-yes). Découple "détecter" de "agir".
type Prompt struct {
    Kind     PromptKind           // Permission | Ready | Trust
    Message  string               // extrait humain pour la TUI (plus tard)
    Resolve  func(Responder) error // nil = rien à faire automatiquement
}

// Adapter porte TOUT le savoir agent-spécifique. Un adapter par agent.
// Ajouter Pi = créer program/pi.go et l'enregistrer. Rien d'autre.
type Adapter interface {
    // Name renvoie l'identifiant canonique (ex. "pi", "claude", "aider").
    Name() string

    // Matches indique si cet adapter gère la commande `program` passée
    // (ex. "/path/to/claude" → adapter claude). Premier match gagne.
    Matches(program string) bool

    // Detect inspecte le contenu du pane tmux et renvoie le statut perçu
    // + un éventuel Prompt à résoudre. Pure function de `content` → testable
    // sans tmux, sans PTY, sans rien.
    Detect(content string) (Status, *Prompt)
}
```

```go
// program/registry.go
package program

var registry []Adapter

func Register(a Adapter) { registry = append(registry, a) }

// Lookup renvoie l'adapter pour `program`, ou un adapter NoOp par défaut
// (StatusUnknown, aucun prompt) — donc un agent inconnu ne crash jamais,
// il est juste "silencieux" : pas de badge Ready, pas d'auto-yes.
func Lookup(program string) Adapter {
    for _, a := range registry {
        if a.Matches(program) {
            return a
        }
    }
    return NoOpAdapter{}
}
```

### Refactor du cœur tmux (le consommateur du seam)

`TmuxSession` garde une référence `adapter program.Adapter` résolue à la
construction (`NewTmuxSession`), et les deux fonctions deviennent des
délégations triviales :

```go
// session/tmux/tmux.go — après refactor
func (t *TmuxSession) HasUpdated() (updated bool, hasPrompt bool) {
    content, err := t.CapturePaneContent()
    if err != nil { return false, false }

    status, prompt := t.adapter.Detect(content)
    hasPrompt = status == StatusReady || status == StatusPermission

    if !bytes.Equal(t.monitor.hash(content), t.monitor.prevOutputHash) {
        t.monitor.prevOutputHash = t.monitor.hash(content)
        return true, hasPrompt
    }
    return false, hasPrompt
}

func (t *TmuxSession) CheckAndHandleTrustPrompt() bool {
    content, err := t.CapturePaneContent()
    if err != nil { return false }

    _, prompt := t.adapter.Detect(content)
    if prompt == nil || prompt.Resolve == nil {
        return false
    }
    if err := prompt.Resolve(t /* implémente Responder */); err != nil {
        log.ErrorLog.Printf("could not resolve prompt: %v", err)
        return false
    }
    return true
}
```

`*TmuxSession` implémente déjà `Responder` via `TapEnter`/`TapDAndEnter`/
`SendKeys` — **aucune nouvelle méthode à écrire côté tmux**. C'est ce qui
garantit que le refactor ne touche pas l'exécution tmux : on ne fait que
déplacer des chaînes.

---

## Étapes (commits atomiques, chacun vert + testé)

### Étape 1 — Créer le package `program/` avec l'interface et un NoOp

**Fichiers :**
- `program/adapter.go` (interface `Adapter`, `Responder`, `Status`, `Prompt`)
- `program/registry.go` (`Register`, `Lookup`, `NoOpAdapter`)

**Tests :**
- `program/registry_test.go` — `Lookup("nimp")` renvoie `NoOpAdapter`,
  `Detect` renvoie `(StatusUnknown, nil)`.

**Commit :** `feat(program): introduce Adapter seam and NoOp default`

Rien ne change encore dans tmux.go — le package est isolé, build passant.

---

### Étape 2 — Porter les 3 agents existants en adapters

**Fichiers (un par agent) :**
- `program/claude.go` + `claude_test.go`
- `program/aider.go`  + `aider_test.go`
- `program/gemini.go` + `gemini_test.go`

Chaque adapter porte les chaînes **déplacées à l'identique** depuis `tmux.go`
(recopie littérale, zéro changement sémantique). Exemple claude :

```go
// program/claude.go
package program

import "strings"

type ClaudeAdapter struct{}

func (ClaudeAdapter) Name() string { return "claude" }
func (ClaudeAdapter) Matches(p string) bool {
    return strings.HasSuffix(p, "claude")
}
func (ClaudeAdapter) Detect(content string) (Status, *Prompt) {
    // trust / MCP → Permission, résolu par TapEnter
    if strings.Contains(content, "Do you trust the files in this folder?") ||
       strings.Contains(content, "new MCP server") {
        return StatusPermission, &Prompt{
            Kind: PromptTrust,
            Resolve: func(r Responder) error { return r.TapEnter() },
        }
    }
    // ready
    if strings.Contains(content, "No, and tell Claude what to do differently") {
        return StatusReady, nil
    }
    return StatusWorking, nil
}
```

**Tests (table-driven, sans tmux, sans PTY) :**
```go
func TestClaudeAdapter_Detect(t *testing.T) {
    cases := []struct{ content string; want Status; wantPrompt bool }{
        {"...Do you trust the files in this folder?...", StatusPermission, true},
        {"...No, and tell Claude what to do differently", StatusReady, false},
        {"random pane content", StatusWorking, false},
    }
    for _, c := range cases {
        s, p := ClaudeAdapter{}.Detect(c.content)
        assert(s == c.want); assert((p != nil) == c.wantPrompt)
    }
}
```

**Commit :** `feat(program): port claude/aider/gemini to Adapter`

Toujours aucun changement dans tmux.go — les chaînes sont dupliquées pour
l'instant (intentionnel : permet de valider les adapters indépendamment
avant le switch).

---

### Étape 3 — Brancher tmux.go sur le registre (le switch)

**Fichier :** `session/tmux/tmux.go`

1. Ajouter un champ `adapter program.Adapter` à `TmuxSession`, résolu dans
   `newTmuxSession` via `program.Lookup(program)`.
2. Réécrire `HasUpdated()` et `CheckAndHandleTrustPrompt()` comme délégations
   (voir snippet plus haut).
3. **Supprimer** les `const ProgramClaude/Aider/Gemini` et les deux `if/else`
   de `tmux.go` — le savoir vit maintenant dans `program/`.
4. Mettre à jour les imports.

**Tests :**
- `session/tmux/tmux_test.go` — existe déjà (`terminal_test.go`), on garde
  les tests de capture-pane ; on ajoute un test qui vérifie qu'un
  `TmuxSession` avec `ClaudeAdapter` détecte bien un prompt trust sur un
  contenu de pane injecté (via une factory d'adapter injectable, déjà
  présente via `NewTmuxSessionWithDeps`).

**Commit :** `refactor(tmux): delegate agent detection to program.Adapter`

C'est le commit qui **bascule** le couplage. Après lui, ajouter un agent ne
touche plus jamais `tmux.go`.

---

### Étape 4 — Enregistrer les adapters au démarrage

**Fichier :** `program/program.go` (ou `init.go`)

```go
package program

func init() {
    Register(ClaudeAdapter{})
    Register(AiderAdapter{})
    Register(GeminiAdapter{})
}
```

L'import de `program/` par `session/tmux` déclenche le `init()` → registre
rempli automatiquement. Aucune glue manuelle ailleurs.

**Commit :** `feat(program): auto-register built-in adapters via init()`

---

### Étape 5 — Ajouter l'adapter Pi (la preuve que le seam fonctionne)

**Fichiers :**
- `program/pi.go` + `pi_test.go`

C'est **le commit qui valide tout le travail** : si l'ajout de Pi se limite à
ce fichier sans toucher au cœur, le seam est réussi.

```go
// program/pi.go
package program

import "strings"

type PiAdapter struct{}

func (PiAdapter) Name() string { return "pi" }
func (PiAdapter) Matches(p string) bool {
    base := p
    if i := strings.LastIndexByte(base, '/'); i >= 0 { base = base[i+1:] }
    return base == "pi"
}

func (PiAdapter) Detect(content string) (Status, *Prompt) {
    // TODO(étape suivante): chaînes exactes du footer/header Pi à valider
    // empiriquement en lançant `pi` dans un tmux et en capturant le pane.
    // Premier critère fiable = ligne de footer "working" vs prompt d'input.
    //
    // Brouillon (à confirmer) :
    // - StatusPermission : prompt de trust "Do you trust" (Pi a un trust prompt)
    //   → Resolve: TapEnter
    // - StatusReady : l'indicateur de travail disparaît du footer
    // - sinon StatusWorking
    return StatusWorking, nil
}
```

**Décision de scoping (à valider avant de coder les chaînes) :**
l'adapter Pi a besoin de chaînes fiables extraite du TUI Pi. Comme on l'a vu
dans la doc Pi, le footer expose coût/tokens/indicateur de travail. La
méthode : lancer `pi` dans une session tmux, capturer le pane avec
`tmux capture-pane -p` à l'état idle puis working, et extraire 1–2 chaînes
stables. **C'est une étape empirique, pas de design** — juste de
l'observation. On laisse l'adapter en `StatusWorking` par défaut tant que
les chaînes ne sont pas confirmées ; Pi ne crashe pas, il est juste silencieux.

**Commit :** `feat(program): add Pi adapter (status detection stub)`

---

### Étape 6 (optionnelle, hors scope strict mais naturelle) — Distinguer
"prompt libre" vs "permission" côté daemon/UI

Aujourd'hui le daemon auto-yes appelle `HasUpdated().hasPrompt` puis
`TapEnter()` aveuglément. Avec la nouvelle interface, `Detect` distingue
`StatusReady` (input libre) de `StatusPermission` (approval). Le daemon peut
alors ne résoudre **que** les `StatusPermission` (auto-approuver les perms
mais ne pas valider un prompt libre) — comportement plus sûr et plus précis.

Ce n'est qu'un ~10 LOC dans `daemon/daemon.go` :
```go
status, prompt := instance.DetectStatus() // nouvelle méthode façade
if status == program.StatusPermission && prompt != nil && prompt.Resolve != nil {
    prompt.Resolve(instance)
}
```

**Commit :** `feat(daemon): resolve only permission prompts in autoyes`

---

## Critères de succès (vérifiables)

1. `go build ./...` et `go test ./...` passent après chaque commit.
2. `tmux.go` ne contient **plus** les chaînes "Claude", "aider", "gemini"
   ni les `const Program*`.
3. Ajouter un agent = **un fichier** dans `program/` + un `Register` dans
   `init()`. Zéro modification de `session/`, `app/`, `daemon/`.
4. La détection d'état des agents existants (claude/aider/gemini) est
   **inchangée en comportement** (mêmes chaînes, même auto-yes) — testé par
   les tests table-driven portés.
5. `cs` lancé avec `-p pi` démarre sans erreur et ne crashe pas sur la
   détection (NoOp → StatusWorking silencieux).

---

## Ce qui est **explicitement hors scope** de ce plan

- Multi-repo (plan séparé : retrait du garde `IsGitRepo(cwd)`, sélecteur de
  repo, branch picker par-instance, regroupement liste).
- Design TUI / métaphore de liste multi-repo.
- Améliorer l'attach plein écran ou le panneau terminal.
- Refactor de la TUI (bubbletea → autre).

Ces sujets viendront chacun avec leur propre plan, après que celui-ci soit
mergé et stabilise le seam agent.
