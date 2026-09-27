/**
 * Multi-instance test: one game played across several API instances that
 * share Postgres and Redis.
 *
 * Run (with `make up-multi`):
 *   k6 run k6/multi_instance.js
 *   k6 run k6/multi_instance.js -e BASE_URLS=http://localhost:8080,http://localhost:8081 \
 *                               -e WS_URLS=ws://localhost:8080,ws://localhost:8081
 *
 * setup() creates a public list and a game on the first instance (with an
 * answer timeout), then joins NUM_PLAYERS - 1 players, alternating instances.
 *
 * One VU = one player; VU 1 is the host's own player. Each player opens its
 * WebSocket on instance (VU - 1) % N and answers every question correctly.
 * The host starts question i through instance i % N. Odd questions are
 * closed by the host through another instance; even ones by the answer
 * deadline, claimed by whichever instance polls first.
 *
 * Every player must receive every question_started, question_closed and a
 * final game_over (no_more_questions), and see all its answers accepted:
 * events and state must flow between instances.
 *
 * Tunables: NUM_PLAYERS (default 10), ANSWER_TIMEOUT (seconds, default 3).
 */
import http from 'k6/http';
import ws from 'k6/ws';
import { check } from 'k6';

const BASE_URLS = (__ENV.BASE_URLS || 'http://localhost:8080,http://localhost:8081').split(',');
const WS_URLS = (__ENV.WS_URLS || 'ws://localhost:8080,ws://localhost:8081').split(',');
const NUM_PLAYERS = parseInt(__ENV.NUM_PLAYERS || '10');
const ANSWER_TIMEOUT = parseInt(__ENV.ANSWER_TIMEOUT || '3');
const NUM_QUESTIONS = 3;

const ADMIN = { 'Content-Type': 'application/json', 'X-Debug-Actor-Type': 'admin', 'X-Debug-Actor-Id': 'k6-multi-admin' };

function actorHeaders(actorID) {
  return { 'Content-Type': 'application/json', 'X-Debug-Actor-Type': 'user', 'X-Debug-Actor-Id': actorID };
}

function instance(i) {
  return BASE_URLS[i % BASE_URLS.length];
}

export const options = {
  scenarios: {
    multi_instance: {
      executor: 'per-vu-iterations',
      vus: NUM_PLAYERS,
      iterations: 1,
      maxDuration: '2m',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    http_req_failed: ['rate==0'],
  },
};

export function setup() {
  const list = http.post(`${instance(0)}/question-lists`,
    JSON.stringify({ name: 'k6 multi-instance', visibility: 'public' }), { headers: ADMIN });
  check(list, { 'setup: list created': (r) => r.status === 201 });
  const listID = list.json('id');
  for (let i = 0; i < NUM_QUESTIONS; i++) {
    const q = http.post(`${instance(i)}/question-lists/${listID}/questions`, JSON.stringify({
      text: `Question ${i + 1}`,
      options: [{ id: 'a', text: 'right' }, { id: 'b', text: 'wrong' }],
      correct_option_id: 'a',
    }), { headers: ADMIN });
    check(q, { 'setup: question added': (r) => r.status === 201 });
  }

  const created = http.post(`${instance(0)}/games`, JSON.stringify({
    owner_name: 'Host', question_list_id: listID, answer_timeout_seconds: ANSWER_TIMEOUT,
  }), { headers: actorHeaders('mp-1') });
  check(created, { 'setup: game created': (r) => r.status === 201 });
  const gameID = created.json('game_id');

  const playerIDs = [created.json('owner_id')];
  for (let i = 2; i <= NUM_PLAYERS; i++) {
    const joined = http.post(`${instance(i)}/games/${gameID}/join`,
      JSON.stringify({ player_name: `Player ${i}` }), { headers: actorHeaders(`mp-${i}`) });
    check(joined, { 'setup: player joined on any instance': (r) => r.status === 200 });
    playerIDs.push(joined.json('player_id'));
  }
  return { gameID, playerIDs };
}

export default function (data) {
  const { gameID, playerIDs } = data;
  const vu = __VU;
  const playerID = playerIDs[vu - 1];
  const actorID = `mp-${vu}`;
  const isHost = vu === 1;
  const wsBase = WS_URLS[(vu - 1) % WS_URLS.length];

  const seen = { started: 0, closed: 0, accepted: 0, rejected: 0, gameOver: '' };

  const res = ws.connect(
    `${wsBase}/ws?gameId=${gameID}&playerId=${playerID}`,
    { headers: { 'X-Debug-Actor-Type': 'user', 'X-Debug-Actor-Id': actorID } },
    function (socket) {
      const start = (index) => {
        const r = http.post(`${instance(index)}/games/${gameID}/start`, null, { headers: actorHeaders(actorID) });
        check(r, { 'host: start through any instance': (x) => x.status === 200 });
      };

      socket.on('message', function (raw) {
        const msg = JSON.parse(raw);
        switch (msg.type) {
          case 'game_joined':
            // Give every player time to connect before the first question.
            if (isHost) socket.setTimeout(() => start(0), 2000);
            break;
          case 'question_started': {
            seen.started++;
            const p = msg.payload;
            socket.send(JSON.stringify({ type: 'submit_answer', data: { question_id: p.question_id, option_id: 'a' } }));
            // Odd questions: the host closes by hand through another instance
            // once everyone had time to answer. Even ones: the deadline closes.
            if (isHost && p.index % 2 === 1) {
              socket.setTimeout(() => {
                const r = http.post(`${instance(p.index + 1)}/games/${gameID}/close`, null, { headers: actorHeaders(actorID) });
                check(r, { 'host: close through another instance': (x) => x.status === 200 });
              }, 1000);
            }
            break;
          }
          case 'answer_submitted':
            if (msg.payload.player_id === playerID) seen.accepted++;
            break;
          case 'answer_rejected':
            seen.rejected++;
            break;
          case 'question_closed':
            seen.closed++;
            if (isHost && msg.payload.remaining_questions > 0) {
              start(seen.closed); // next question through the next instance
            }
            break;
          case 'game_over':
            seen.gameOver = msg.payload.reason;
            socket.close();
            break;
        }
      });

      socket.setTimeout(() => socket.close(), (NUM_QUESTIONS * (ANSWER_TIMEOUT + 3) + 10) * 1000);
    },
  );

  check(res, { 'ws: connected (101)': (r) => r && r.status === 101 });
  check(seen, {
    'every question_started received': (s) => s.started === NUM_QUESTIONS,
    'every question_closed received': (s) => s.closed === NUM_QUESTIONS,
    'every answer accepted': (s) => s.accepted === NUM_QUESTIONS && s.rejected === 0,
    'game over after the last question': (s) => s.gameOver === 'no_more_questions',
  });
}
