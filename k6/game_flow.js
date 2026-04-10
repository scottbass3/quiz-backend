/**
 * Load test: full game lifecycle — HTTP + WebSocket.
 *
 * Run: k6 run k6/game_flow.js
 *      k6 run k6/game_flow.js -e BASE_URL=http://api:8080 -e WS_URL=ws://api:8080
 *
 * setup() creates one public question list with 5 questions once.
 *
 * Each VU iteration:
 *   1. POST /games                     → create game (1 player = owner)
 *   2. WS /ws?gameId=...&playerId=...  → connect
 *   3. ← game_joined                  → POST /games/{id}/start
 *   4. ← question_started             → submit_answer via WS
 *   5.                                → POST /games/{id}/close (after 500ms)
 *   6. ← question_closed + game_over  → socket.close()
 *
 * Note: with a single active player, CloseQuestion always triggers game_over
 * (len(activePlayers) = 1 ≤ 1). The full HTTP+WS path is exercised every iteration.
 */
import http from 'k6/http';
import ws from 'k6/ws';
import { check, sleep } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const WS_URL = __ENV.WS_URL || 'ws://localhost:8080';

const ADMIN_HEADERS = {
  'Content-Type': 'application/json',
  'X-Debug-Actor-Type': 'admin',
  'X-Debug-Actor-Id': 'k6-game-admin',
};

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
    game_flow: {
      executor: 'constant-vus',
      vus: 10,
      duration: '2m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
    ws_connecting: ['p(95)<300'],
  },
};

export function setup() {
  const listRes = http.post(
    `${BASE_URL}/question-lists`,
    JSON.stringify({
      name: 'k6 Game Flow Test',
      description: 'Created by k6 load test',
      visibility: 'public',
    }),
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

  return { questionListID: listID };
}

export default function (data) {
  const { questionListID } = data;
  const vuID = `vu-${__VU}-${__ITER}`;

  const userHeaders = {
    'Content-Type': 'application/json',
    'X-Debug-Actor-Type': 'user',
    'X-Debug-Actor-Id': vuID,
  };

  // Step 1: Create a game.
  const gameRes = http.post(
    `${BASE_URL}/games`,
    JSON.stringify({ owner_name: `Player ${vuID}`, question_list_id: questionListID }),
    { headers: userHeaders }
  );
  check(gameRes, { 'game created': (r) => r.status === 201 });
  if (gameRes.status !== 201) {
    return;
  }
  const { game_id: gameID, owner_id: ownerID } = gameRes.json();

  // Step 2: Connect via WebSocket and play one full question cycle.
  const wsRes = ws.connect(
    `${WS_URL}/ws?gameId=${gameID}&playerId=${ownerID}`,
    {
      headers: {
        'X-Debug-Actor-Type': 'user',
        'X-Debug-Actor-Id': vuID,
      },
    },
    function (socket) {
      socket.on('message', function (raw) {
        const msg = JSON.parse(raw);

        // Received game_joined → start the first question.
        if (msg.type === 'game_joined') {
          http.post(`${BASE_URL}/games/${gameID}/start`, null, { headers: userHeaders });
        }

        // Received question_started → submit an answer, then close the question.
        if (msg.type === 'question_started') {
          const questionID = msg.payload.question_id;
          const optionID = msg.payload.options[0].id;

          socket.send(
            JSON.stringify({
              type: 'submit_answer',
              data: { question_id: questionID, option_id: optionID },
            })
          );

          // Give the server 500ms to record the answer before closing.
          socket.setTimeout(function () {
            http.post(`${BASE_URL}/games/${gameID}/close`, null, { headers: userHeaders });
          }, 500);
        }

        // game_over fires after CloseQuestion when only 1 active player remains.
        if (msg.type === 'game_over') {
          socket.close();
        }
      });

      socket.on('error', function (e) {
        console.error(`ws error [${vuID}]: ${e}`);
      });

      // Safety valve: close if the game stalls for any reason.
      socket.setTimeout(function () {
        socket.close();
      }, 15000);
    }
  );

  check(wsRes, { 'ws: status 101': (r) => r && r.status === 101 });

  sleep(0.5);
}
