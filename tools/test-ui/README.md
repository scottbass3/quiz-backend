# quizz test ui

Outil de test interne pour le backend `quizz-backend`.  
**Pas une UI de production.** Sert uniquement à simuler des joueurs et vérifier les flux HTTP + WebSocket manuellement.

## Démarrage rapide

### Avec Docker Compose (recommandé)

```bash
# Depuis la racine du projet
make up          # démarre api + postgres + redis + ui
make migrate-up  # applique les migrations SQL
```

L'UI est disponible sur **http://localhost:5173**

```bash
make ui-logs     # voir les logs Vite
```

### Sans Docker (Vite local)

Prérequis : Node 18+ et le backend qui tourne sur `localhost:8080`.

```bash
cd tools/test-ui
cp .env.example .env    # VITE_API_TARGET=http://localhost:8080
npm install
npm run dev
```

## Configuration

| Variable          | Description                              | Défaut                    |
|-------------------|------------------------------------------|---------------------------|
| `VITE_API_TARGET` | URL du backend côté serveur Vite (proxy) | `http://localhost:8080`   |
| `UI_PORT`         | Port exposé par Docker Compose           | `5173`                    |

> `VITE_API_TARGET` est utilisée **côté serveur Vite** pour le proxy, pas dans le bundle.  
> Le navigateur communique uniquement avec le serveur Vite via `/api/*` et `/ws`.

## Flux de test typique

```
1. Cliquer "GET /health"           → vérifier que le backend répond
2. Saisir un nom d'hôte et cliquer "create"  → récupérer le game_id
3. (optionnel) cliquer "GET" pour voir l'état initial
4. Ajouter 2-3 questions via le formulaire
5. Pour chaque carte joueur :
   - Cliquer "join" (le game_id est automatiquement partagé)
   - Cliquer "connect ws"
6. Cliquer "▶ start question" → tous les joueurs reçoivent question_started
7. Chaque carte joueur voit la question et des boutons A/B/C/D
8. Soumettre des réponses → voir answer_submitted dans les logs d'événements
9. Cliquer "■ close question" → voir life_lost, player_eliminated, game_over
10. Les logs HTTP en bas de page montrent chaque requête avec body/response
```

## Structure

```
tools/test-ui/
├── index.html
├── package.json
├── tsconfig.json
├── vite.config.ts        — proxy /api/* et /ws vers le backend
├── .env.example
└── src/
    ├── main.ts
    ├── App.vue            — layout principal, état gameId + slots joueurs
    ├── style.css          — thème terminal sombre minimal
    ├── types.ts           — interfaces partagées
    ├── api.ts             — couche HTTP (fetch + logging automatique)
    ├── debug.ts           — store réactif pour les logs HTTP
    └── components/
        ├── HealthPanel.vue   — GET /health
        ├── GamePanel.vue     — créer partie, charger état, add question, start/close
        ├── PlayerCard.vue    — join, WS, question active, log d'événements
        └── DebugPanel.vue    — log HTTP, config runtime
```
