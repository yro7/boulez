# Roadmap & Ideas — cs2

> Idées repoussées (pas au scope actuel) et décisions produits en attente.
> Ce fichier est un parking, pas un plan d'action. Rien ici n'est engagé.

---

## Idées repoussées

### Import one-shot des repos depuis VS Code

**Contexte.** Lors de la discussion sur la source des repos connus pour le
multi-repo, l'idée est venue de réutiliser la liste de dossiers récemment
ouverts que VS Code maintient, pour amorcer le registre cs2 sans saisie
manuelle.

**Ce que VS Code maintient réellement (important — idée reçue).** VS Code
ne maintient PAS une liste de repos git. Il a :

- "Open Recent" = liste de *dossiers* récemment ouverts, pas spécifiquement
  git. Stockée dans `~/Library/Application Support/Code/User/globalStorage/storage.json`
  (macOS), format interne non stable, susceptible de casser à chaque update.
- Panneau Source Control : détecte le git du workspace *ouvert*, point.
  Pas de registre global.
- GitLens / extensions tierces : peuvent lister des repos, mais par extension.

Donc "récupérer les repos de VS Code" = en pratique parser le `storage.json`
des dossiers récents, puis filtrer pour ne garder que ceux qui sont des repos
git.

**Décision.** Ne PAS coupler cs2 à VS Code en continu (lecture à chaque
démarrage). Raisons : format non documenté et instable, support macOS vs
Linux paths divergents, dépendance à ce que l'utilisateur utilise VS Code —
tout cela contredit le principe "standalone, agent-agnostic" du fork
(voir `AGENTS.md`).

**Idée repoussée (à réévaluer si besoin).** À la place, un import *one-shot*,
lancé manuellement :

```
cs2 repo import-vscode
```

- Parse `storage.json` une seule fois, à la demande de l'utilisateur.
- Filtre les chemins pour ne garder que les repos git (`git.IsGitRepo`).
- Peuple le registre cs2 avec les résultats.
- C'est un import ponctuel, pas un couplage permanent : si le format VS Code
  change, l'import casse (ou se vide) mais cs2 tourne toujours.
- À isoler dans un fichier dédié (`config/vscode_import.go` ou similaire)
  pour garder la fragilité contenue.

**Pourquoi ce n'est pas implémenté maintenant.** Le registre cs2 auto-rempli
à l'usage suffit pour démarrer le multi-repo. L'import VS Code n'est qu'un
confort d'amorçage ; il sera pertinent uniquement si l'utilisateur part de
zéro avec beaucoup de repos déjà connus de VS Code. À réévaluer à ce moment.

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
