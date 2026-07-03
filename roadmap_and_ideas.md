# Roadmap & Ideas — cs2

> Idées repoussées (pas au scope actuel), décisions produits en attente,
> et choses implémentées. Ce fichier est un parking, pas un plan d'action.
> Rien ici n'est engagé.

---

## Implémenté

### Import one-shot des repos depuis les IDE (VS Code et forks)

**Commande.** `cs2 repo-import` (avec `--dry-run` et `--ide <name>`).
Code isolé dans le package `ideimport/`.

**Contexte / idée reçue.** VS Code ne maintient PAS une liste de repos git.
Il a une liste de *dossiers* récemment ouverts (« Open Recent »), stockée
dans `~/Library/Application Support/<IDE>/User/globalStorage/storage.json`
(macOS), format interne non stable. Donc « récupérer les repos de VS Code » =
en pratique parser ce `storage.json`, puis filtrer pour ne garder que les
repos git (`git.IsGitRepo`).

**Décision produit.** Import *one-shot, manuel* — jamais de couplage permanent
(lecture à chaque démarrage interdite). Raisons : format non documenté et
instable, dépendance à ce que l'utilisateur utilise un IDE VS Code-fork —
tout cela contredirait le principe « standalone, agent-agnostic » du fork.
Si le format IDE casse, l'import échoue ou se vide, mais cs2 tourne toujours.

**Implémentation.**

- Scan tous les IDEs VS Code-forks installés par défaut, ou un seul via
  `--ide` : `vscode`, `cursor`, `windsurf`, `antigravity`, `vscodium`,
  `pearai`, `void`, `trae`.
- Parse robuste : **walk récursif du JSON**, ne dépend d'aucun nom de clé —
  survit aux variations de format entre IDEs et versions.
- Ne collecte que les URLs `file://` (les paths absolus nus sont ignorés pour
  éviter de récolter machine IDs / tokens — AGENTS.md : pas de fuite sensible).
- `--dry-run` lit le registre en lecture seule pour distinguer nouveau vs déjà
  connu, sans rien écrire.
- Écriture idempotente via `Registry.Add` (dédup + résolution absolue).

---

## Idées repoussées

*(Aucune idée repoussée pour l'instant — l'import IDE, anciennement ici, a
été implémenté, voir ci-dessus.)*

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
