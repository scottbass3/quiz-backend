# quizz test UI

Outil de test interne pour le backend `quizz-backend`.
**Pas une UI de production.** Il sert à piloter l'API à la main : choisir une identité de test, gérer les listes de questions, créer une partie, simuler des joueurs et inspecter chaque requête HTTP et chaque événement WebSocket.

## Démarrage

### Avec Docker Compose (recommandé)

```bash
# Depuis la racine du projet
make up        # démarre api, postgres, redis et l'UI
make ui-logs   # logs Vite
```

L'UI est disponible sur **http://localhost:5173** (port configurable via `UI_PORT`). Les migrations sont appliquées automatiquement au démarrage de l'API.

### Sans Docker

Prérequis : Node 18+ et le backend accessible (par défaut sur `localhost:8080`).

```bash
cd tools/test-ui
cp .env.example .env    # VITE_API_TARGET=http://localhost:8080
npm install
npm run dev
```

## Configuration

| Variable          | Description                                   | Défaut                  |
|-------------------|-----------------------------------------------|-------------------------|
| `VITE_API_TARGET` | URL du backend utilisée par le proxy Vite     | `http://localhost:8080` |
| `UI_PORT`         | Port exposé par Docker Compose                | `5173`                  |

`VITE_API_TARGET` est lue **côté serveur Vite**, elle n'est pas injectée dans le bundle. Le navigateur ne parle qu'au serveur Vite, qui relaie `/api/*` (préfixe retiré) et `/ws` vers le backend. Il n'y a donc pas de problème de CORS, et le cookie de session OIDC est partagé entre l'UI et l'API.

## Identité (acteur)

La barre en haut de la colonne de gauche s'adapte au mode d'authentification du backend (lu via `GET /auth/me`) :

- **Mode dev** (`OIDC_ENABLED=false`) : on choisit le type (`admin` ou `user`) et l'ID de l'acteur. Ils sont envoyés dans les en-têtes `X-Debug-Actor-*` de chaque requête HTTP. Pour le WebSocket, le navigateur ne pouvant pas envoyer d'en-têtes, chaque carte joueur passe l'acteur avec lequel elle a rejoint la partie en paramètres `debugActorType` et `debugActorId`.
- **Mode OIDC** (`OIDC_ENABLED=true`) : lien de connexion vers `/api/auth/login`, puis affichage de l'utilisateur connecté et bouton de déconnexion.

Règles côté backend à garder en tête :

- Seul l'acteur qui a **créé la partie** (l'hôte) peut démarrer et clôturer les questions.
- Une carte joueur ne peut se connecter en WebSocket qu'avec l'acteur qui a fait le **join**.

Il faut donc garder le même acteur entre la création de la partie, les joins et le jeu.

## Flux de test typique

1. `GET /health` pour vérifier que le backend répond.
2. Onglet **Lists** : en `admin`, créer une liste publique et y ajouter quelques questions (ou, en `user`, une liste privée).
3. Passer en `user` si besoin, sélectionner la liste, cliquer sur **use in game →**, puis créer la partie dans l'onglet **Game**. On peut y régler le nombre de vies et un temps limite de réponse en secondes (`0` : pas de limite, l'hôte clôt à la main). Le `game_id` est partagé automatiquement avec les cartes joueurs.
4. Ajouter des cartes joueurs (6 au maximum), puis pour chacune : **join**, puis **connect ws**.
5. **▶ start question** : chaque carte reçoit `question_started` et affiche les options. La dernière question de la liste est signalée par « last question ».
6. Répondre depuis les cartes : `answer_submitted` apparaît dans les journaux d'événements.
7. **■ close question** (ou fin du temps limite) : `question_closed`, puis `life_lost`, `player_eliminated` et `game_over` selon le cas. Le résumé (vies perdues, éliminés, nombre de questions restantes) s'affiche sous les boutons.
8. Recommencer jusqu'à `game_over`. Chaque carte affiche alors la raison (dernier joueur en vie, tout le monde éliminé, plus de questions) et l'issue (victoire, vainqueur, égalité ou aucun vainqueur). Un nouveau **start** après la dernière question renvoie une erreur `409` avec le code `no_more_questions`.

À noter : le créateur de la partie est aussi un joueur (le joueur « owner ») mais n'a pas de carte. Il ne répond jamais et perd donc une vie à chaque question.

Le panneau du bas liste chaque requête HTTP avec son corps et sa réponse. Cliquer sur un événement d'une carte joueur affiche son payload complet.

## Thèmes

- Onglet **Themes** : thèmes globaux (création, renommage, suppression). Seuls les `admin` peuvent les modifier.
- Onglet **Lists**, sous la liste sélectionnée : thèmes propres à cette liste, modifiables par ceux qui peuvent éditer la liste (un `admin` pour une liste publique, le propriétaire pour une liste privée).
- Le formulaire de question propose un thème (aucun, global ou de la liste). Le bouton **edit** d'une question recharge le formulaire pour la modifier, y compris son thème.
- La liste des questions peut être filtrée par thème (« no theme » pour les questions sans thème). Les thèmes s'affichent en bleu (global) ou en orange (liste), sur les questions et sur la question en cours des cartes joueurs.
- Supprimer un thème laisse ses questions sans thème. Après une modification dans l'onglet **Themes**, cliquer sur **refresh** au-dessus des questions pour mettre leur affichage à jour.

## Structure

```
tools/test-ui/
├── index.html
├── package.json
├── tsconfig.json
├── vite.config.ts             proxy /api/* et /ws vers le backend
├── .env.example
└── src/
    ├── main.ts
    ├── App.vue                layout, onglets Lists / Themes / Game, cartes joueurs
    ├── style.css              thème sombre minimal
    ├── types.ts               types partagés
    ├── api.ts                 couche HTTP (fetch, en-têtes d'acteur, journalisation)
    ├── actor.ts               acteur de debug et session OIDC (/auth/me)
    ├── debug.ts               store réactif des journaux HTTP
    ├── themes.ts              thèmes globaux partagés entre l'onglet Themes et le formulaire de question
    └── components/
        ├── ActorBar.vue            choix de l'acteur (dev) ou session OIDC
        ├── HealthPanel.vue         GET /health
        ├── QuestionListsPanel.vue  listes, questions (ajout, édition, thème, filtre)
        ├── ThemesPanel.vue         gestion d'une portée de thèmes (globale ou d'une liste)
        ├── GamePanel.vue           créer une partie, état, start / close
        ├── PlayerCard.vue          join, WebSocket, question active, journal d'événements
        └── DebugPanel.vue          journal HTTP, configuration
```
