// Transport smoke + speed benchmark against one server.
// Usage: node wsbench.mjs <port> [image] [board] [--bench-only]
// image: absolute path for the Go server; 'TEST.BIN' form for the legacy Node server.
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const BASE = `http://127.0.0.1:${process.argv[2] ?? 8902}`;
const WSURL = `ws://127.0.0.1:${process.argv[2] ?? 8902}/ws`;
const IMAGE = process.argv[3] && !process.argv[3].startsWith('--') ? process.argv[3] : path.resolve(HERE, '..', 'TEST.BIN');
const BOARD = process.argv[4] && !process.argv[4].startsWith('--') ? process.argv[4] : 'auto';
const benchOnly = process.argv.includes('--bench-only');
let failures = 0;
const ok = (label, cond, extra = '') => {
  console.log(`${cond ? 'ok' : 'FAIL'} ${label} ${extra}`);
  if (!cond) failures++;
};

async function post(route, body) {
  const res = await fetch(BASE + route, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body ?? {}) });
  return { status: res.status, data: await res.json() };
}
async function get(route) {
  const res = await fetch(BASE + route);
  return { status: res.status, data: await res.json() };
}

if (!benchOnly) {
  // --- WebSocket protocol ---
  const ws = new WebSocket(WSURL);
  const messages = [];
  let closed = null;
  const opened = new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  ws.onmessage = e => messages.push(JSON.parse(e.data));
  ws.onclose = e => { closed = { code: e.code, reason: e.reason }; };
  await opened;
  await new Promise(r => setTimeout(r, 300));
  ok('ws initial state push', messages.some(m => m.type === 'state' && m.data?.cpu && m.data?.nccState && m.data?.patchManifest !== undefined));

  ws.send(JSON.stringify({ type: 'subscribe', visible: true, debugger: true, address: null }));
  ws.send(JSON.stringify({ type: 'request', id: 1, route: 'state', method: 'GET' }));
  ws.send(JSON.stringify({ type: 'request', id: 2, route: 'devices', method: 'POST', body: { type: 'key', data: { key: '7' } } }));
  ws.send(JSON.stringify({ type: 'request', id: 3, route: 'run', method: 'POST', body: { instructions: 1000 } }));
  ws.send(JSON.stringify({ type: 'request', id: 4, route: 'bogus/route', method: 'GET' }));
  ws.send(JSON.stringify({ type: 'request', id: 5, route: 'firmware/export', method: 'GET' }));
  ws.send(JSON.stringify({ type: 'request', id: 6, route: 'speed', method: 'POST', body: { factor: 'x' } }));
  await new Promise(r => setTimeout(r, 700));
  const resp = id => messages.find(m => m.type === 'response' && m.id === id);
  ok('ws rpc state', resp(1)?.status === 200 && resp(1)?.data?.cpu !== undefined);
  ok('ws rpc devices key', resp(2)?.status === 200 && resp(2)?.data?.devices !== undefined);
  ok('ws rpc run', resp(3)?.status === 200 && resp(3)?.data?.execution?.steps === 1000);
  ok('ws rpc 404 route', resp(4)?.status === 404 && resp(4)?.data?.error === 'Unknown API endpoint');
  ok('ws rpc http-only export', resp(5)?.status === 400 && resp(5)?.data?.error === 'This endpoint requires HTTP');
  ok('ws rpc validation 400', resp(6)?.status === 400 && resp(6)?.data?.error === 'factor must be a number from 0.1 to 64');
  ok('ws pushes events/display/debugger',
    messages.some(m => m.type === 'events') && messages.some(m => m.type === 'display') && messages.some(m => m.type === 'debugger'));
  ws.close();
  await new Promise(r => setTimeout(r, 200));

  // invalid message type closes 1008
  const ws3 = new WebSocket(WSURL);
  let closed3 = null;
  ws3.onclose = e => { closed3 = { code: e.code, reason: e.reason }; };
  await new Promise(r => { ws3.onopen = r; });
  ws3.send(JSON.stringify({ type: 'nonsense' }));
  await new Promise(r => setTimeout(r, 300));
  ok('ws invalid message closes 1008', closed3?.code === 1008, JSON.stringify(closed3));

  // bad JSON closes 1007
  const ws2 = new WebSocket(WSURL);
  let closed2 = null;
  ws2.onclose = e => { closed2 = e.code; };
  await new Promise(r => { ws2.onopen = r; });
  ws2.send('{not json');
  await new Promise(r => setTimeout(r, 300));
  ok('ws bad json closes 1007', closed2 === 1007, String(closed2));

  // --- SSE ---
  const ctrl = new AbortController();
  const sseRes = await fetch(BASE + '/api/events/stream?display=1', { signal: ctrl.signal });
  ok('sse content-type', sseRes.headers.get('content-type') === 'text/event-stream');
  const reader = sseRes.body.getReader();
  const { value } = await reader.read();
  const text = new TextDecoder().decode(value);
  ok('sse initial state event', text.startsWith('event: state\ndata: {') && text.includes('"cpu"'));
  ctrl.abort();

  // --- static frontend ---
  const idx = await fetch(BASE + '/');
  ok('static index.html', idx.status === 200 && idx.headers.get('content-type').includes('text/html'));
  const appjs = await fetch(BASE + '/app.js');
  ok('static app.js', appjs.status === 200 && appjs.headers.get('content-type') === 'text/javascript');
  const openapi = await fetch(BASE + '/openapi.json');
  const spec = await openapi.json();
  ok('openapi served', spec.openapi === '3.1.0' && Object.keys(spec.paths).length > 30);

  // --- origin rejection ---
  const cross = await fetch(BASE + '/api/state', { headers: { Origin: 'http://evil.example' } });
  ok('cross-origin rejected 403', cross.status === 403);
}

// --- speed benchmark: bounded run ---
await post('/api/load', { path: IMAGE, board: BOARD });
await post('/api/reset', { cold: true });
const st = await get('/api/state');
const clockHz = st.data.board.clockHz;
console.log(`bench image=${st.data.firmware?.path} board=${st.data.board.id} clock=${clockHz}`);
const N = 2000000;
let t0 = performance.now();
const run = await post('/api/run', { instructions: N });
const dt = performance.now() - t0;
const cycles = run.data.execution.cycles;
const instrRate = N / (dt / 1000);
const cycleRate = cycles / (dt / 1000);
const realtime = cycleRate / clockHz;
console.log(`BENCH bounded: ${N} instrs in ${dt.toFixed(0)}ms -> ${(instrRate / 1e6).toFixed(2)}M instr/s, ${(cycleRate / 1e6).toFixed(1)}M cycles/s = ${realtime.toFixed(1)}x realtime (reason=${run.data.execution.reason})`);

// --- realtime loop: resume at 64x and measure achieved factor ---
await post('/api/reset', { cold: true });
await post('/api/speed', { factor: 64 });
await post('/api/resume', {});
await new Promise(r => setTimeout(r, 4000));
const speed = await get('/api/speed');
await post('/api/pause', {});
console.log(`BENCH realtime: target 64x, achieved ${speed.data.achievedFactor.toFixed(1)}x`);
ok('realtime target reached', speed.data.achievedFactor >= 60, `achieved=${speed.data.achievedFactor}`);

console.log(failures ? `FAILURES: ${failures}` : 'ALL OK');
process.exit(failures ? 1 : 0);
