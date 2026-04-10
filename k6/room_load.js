/**
 * Load test: large room — N players in a single game.
 *
 * One VU = one player. All VUs share the same game created in setup().
 * VU 1 = game master (starts and closes questions via HTTP).
 * All VUs = players (connect WebSocket, submit answers).
 *
 * Run:
 *   k6 run k6/room_load.js
 *   k6 run k6/room_load.js -e NUM_PLAYERS=100
 *   k6 run k6/room_load.js -e NUM_PLAYERS=200 -e BASE_URL=http://api:8080 -e WS_URL=ws://api:8080
 *
 * Tunables:
 *   NUM_PLAYERS       — number of concurrent players in the room (default: 50)
 *   START_DELAY_MS    — how long the game master waits after game_joined before
 *                       starting Q1, to allow all VUs to connect their WS (default: 3000)
 *   ANSWER_MIN_MS     — minimum time a player waits before submitting an answer (default: 200)
 *   ANSWER_MAX_MS     — maximum time a player waits before submitting an answer (default: 2000)
 *   CLOSE_DELAY_MS    — extra time the game master waits on top of ANSWER_MAX_MS before
 *                       closing the question (default: 500)
 */
import http from 'k6/http';
import ws from 'k6/ws';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const WS_URL = __ENV.WS_URL || 'ws://localhost:8080';
const NUM_PLAYERS = parseInt(__ENV.NUM_PLAYERS || '50');
const START_DELAY_MS = parseInt(__ENV.START_DELAY_MS || '3000');
const ANSWER_MIN_MS = parseInt(__ENV.ANSWER_MIN_MS || '200');
const ANSWER_MAX_MS = parseInt(__ENV.ANSWER_MAX_MS || '2000');
const CLOSE_DELAY_MS = parseInt(__ENV.CLOSE_DELAY_MS || '500');

function randomAnswerDelay() {
  return Math.floor(Math.random() * (ANSWER_MAX_MS - ANSWER_MIN_MS + 1)) + ANSWER_MIN_MS;
}

const ADMIN_HEADERS = {
  'Content-Type': 'application/json',
  'X-Debug-Actor-Type': 'admin',
  'X-Debug-Actor-Id': 'k6-admin',
};

function userHeaders(actorID) {
  return {
    'Content-Type': 'application/json',
    'X-Debug-Actor-Type': 'user',
    'X-Debug-Actor-Id': actorID,
  };
}

const QUESTIONS = [
  {
    text: 'What is 2 + 2?',
    options: [
      { id: 'q1a', text: '3' },
      { id: 'q1b', text: '4' },
      { id: 'q1c', text: '5' },
      { id: 'q1d', text: '6' },
    ],
    correct_option_id: 'q1b',
  },
  {
    text: 'Which planet is closest to the Sun?',
    options: [
      { id: 'q2a', text: 'Venus' },
      { id: 'q2b', text: 'Earth' },
      { id: 'q2c', text: 'Mercury' },
      { id: 'q2d', text: 'Mars' },
    ],
    correct_option_id: 'q2c',
  },
  {
    text: 'What is the capital of France?',
    options: [
      { id: 'q3a', text: 'Berlin' },
      { id: 'q3b', text: 'Madrid' },
      { id: 'q3c', text: 'Rome' },
      { id: 'q3d', text: 'Paris' },
    ],
    correct_option_id: 'q3d',
  },
  {
    text: 'How many sides does a hexagon have?',
    options: [
      { id: 'q4a', text: '5' },
      { id: 'q4b', text: '6' },
      { id: 'q4c', text: '7' },
      { id: 'q4d', text: '8' },
    ],
    correct_option_id: 'q4b',
  },
  {
    text: 'Who wrote Romeo and Juliet?',
    options: [
      { id: 'q5a', text: 'Charles Dickens' },
      { id: 'q5b', text: 'Jane Austen' },
      { id: 'q5c', text: 'William Shakespeare' },
      { id: 'q5d', text: 'Homer' },
    ],
    correct_option_id: 'q5c',
  },
];

export const options = {
  scenarios: {
    room_load: {
      // Each VU runs exactly once. The test ends when all VUs finish.
      executor: 'per-vu-iterations',
      vus: NUM_PLAYERS,
      iterations: 1,
      maxDuration: '3m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
    ws_connecting: ['p(95)<500'],
  },
};

export function setup() {
  // Create the question list.
  const listRes = http.post(
    `${BASE_URL}/question-lists`,
    JSON.stringify({ name: 'k6 Room Load Test', description: '', visibility: 'public' }),
    { headers: ADMIN_HEADERS }
  );
  check(listRes, { 'setup: list created': (r) => r.status === 201 });
  const listID = listRes.json('id');

  for (const q of QUESTIONS) {
    const res = http.post(
      `${BASE_URL}/question-lists/${listID}/questions`,
      JSON.stringify(q),
      { headers: ADMIN_HEADERS }
    );
    check(res, { 'setup: question added': (r) => r.status === 201 });
  }

  // Create the game. The owner becomes player-1 (VU 1 / game master).
  const gameRes = http.post(
    `${BASE_URL}/games`,
    JSON.stringify({ owner_name: 'Player 1', question_list_id: listID }),
    { headers: userHeaders('player-1') }
  );
  check(gameRes, { 'setup: game created': (r) => r.status === 201 });
  const { game_id: gameID, owner_id: ownerID } = gameRes.json();

  // Pre-join all remaining players before any VU starts.
  // This guarantees the game is fully populated when VUs begin connecting.
  const playerIDs = [ownerID];
  for (let i = 2; i <= NUM_PLAYERS; i++) {
    const joinRes = http.post(
      `${BASE_URL}/games/${gameID}/join`,
      JSON.stringify({ player_name: `Player ${i}` }),
      { headers: userHeaders(`player-${i}`) }
    );
    check(joinRes, { 'setup: player joined': (r) => r.status === 200 });
    playerIDs.push(joinRes.json('player_id'));
  }

  return { gameID, playerIDs };
}

export default function (data) {
  const { gameID, playerIDs } = data;

  // __VU is 1-indexed; playerIDs[0] is the owner (game master).
  const playerID = playerIDs[__VU - 1];
  const actorID = `player-${__VU}`;
  const isGameMaster = __VU === 1;

  const wsRes = ws.connect(
    `${WS_URL}/ws?gameId=${gameID}&playerId=${playerID}`,
    { headers: { 'X-Debug-Actor-Type': 'user', 'X-Debug-Actor-Id': actorID } },
    function (socket) {
      let questionIndex = -1;
      let totalQuestions = 0;

      socket.on('message', function (raw) {
        const msg = JSON.parse(raw);

        // ── game_joined ────────────────────────────────────────────────────
        if (msg.type === 'game_joined') {
          totalQuestions = msg.payload.total_questions;

          if (isGameMaster) {
            // Wait for all VUs to have established their WS connection before
            // starting Q1. Other VUs connect concurrently; this delay is the
            // synchronisation point.
            socket.setTimeout(function () {
              http.post(`${BASE_URL}/games/${gameID}/start`, null, { headers: userHeaders(actorID) });
            }, START_DELAY_MS);
          }
        }

        // ── question_started ───────────────────────────────────────────────
        if (msg.type === 'question_started') {
          const idx = msg.payload.index;
          questionIndex = idx;

          // Every player submits an answer after a random delay (simulates think time).
          const submitPayload = JSON.stringify({
            type: 'submit_answer',
            data: {
              question_id: msg.payload.question_id,
              option_id: msg.payload.options[0].id,
            },
          });
          socket.setTimeout(function () {
            socket.send(submitPayload);
          }, randomAnswerDelay());

          if (isGameMaster) {
            // Close the question after ANSWER_MAX_MS + CLOSE_DELAY_MS, giving
            // all players time to submit within their random answer window.
            socket.setTimeout(function () {
              const closeRes = http.post(
                `${BASE_URL}/games/${gameID}/close`,
                null,
                { headers: userHeaders(actorID) }
              );
              check(closeRes, { 'close question: 200': (r) => r.status === 200 });
              if (closeRes.status !== 200) return;

              const body = closeRes.json();
              if (!body.game_over && idx + 1 < totalQuestions) {
                http.post(`${BASE_URL}/games/${gameID}/start`, null, { headers: userHeaders(actorID) });
              }
            }, ANSWER_MAX_MS + CLOSE_DELAY_MS);
          }
        }

        // ── question_closed ────────────────────────────────────────────────
        // All questions have been played: every VU closes its own socket.
        if (msg.type === 'question_closed' && questionIndex + 1 >= totalQuestions) {
          socket.close();
        }

        // ── game_over ──────────────────────────────────────────────────────
        // Fired when active players drop to ≤ 1 (players lost all their lives).
        if (msg.type === 'game_over') {
          socket.close();
        }
      });

      socket.on('error', function (e) {
        console.error(`ws error [${actorID}]: ${e}`);
      });

      // Safety valve: close if the game stalls for any reason.
      socket.setTimeout(function () {
        socket.close();
      }, 60000);
    }
  );

  check(wsRes, { 'ws: status 101': (r) => r && r.status === 101 });
}
