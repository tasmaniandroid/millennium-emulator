// Differential checker: runs identical bounded workloads against two
// API-compatible servers (historically the retired Node oracle vs the Go
// implementation; also useful Go-vs-Go for regressions), then compares
// state, memory, cards, events and disassembly.
// Usage: node diffcheck.mjs [portA] [portB] [board] [imageA] [imageB]
//   imageA defaults to the Node-alias form, imageB to an absolute path.
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const BUILD = path.resolve(HERE, '..');
const A = `http://127.0.0.1:${process.argv[2] ?? 8901}`;
const B = `http://127.0.0.1:${process.argv[3] ?? 8902}`;
const BOARD = process.argv[4] ?? 'auto';
const defaults = {
  auto: ['TEST.BIN', path.join(BUILD, 'TEST.BIN')],
  mtr1: ['TEST.BIN', path.join(BUILD, 'TEST.BIN')],
  mtr2: ['@tty/flash.bin', path.resolve(BUILD, '..', '..', 'TTY', 'flash.bin')],
};
const IMAGE_A = process.argv[5] ?? (defaults[BOARD]?.[0] ?? 'TEST.BIN');
const IMAGE_B = process.argv[6] ?? (defaults[BOARD]?.[1] ?? path.join(BUILD, 'TEST.BIN'));
const STEPS = [1, 1000, 50000, 200000, 500000];

async function post(base, route, body) {
  const res = await fetch(base + route, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body ?? {}) });
  return { status: res.status, data: await res.json() };
}
async function get(base, route) {
  const res = await fetch(base + route);
  return { status: res.status, data: await res.json() };
}
function normalize(obj, dropRestoreArtifacts) {
  const o = JSON.parse(JSON.stringify(obj));
  delete o.view; delete o.realtime;
  // Paths legitimately differ between servers (aliases vs absolute).
  if (o.firmware) delete o.firmware.path;
  if (o.symbols) delete o.symbols.map;
  if (dropRestoreArtifacts) { delete o.eventId; delete o.stopReason; }
  return o;
}
function normalizeEvents(events) {
  // firmware.loaded carries the load path, which legitimately differs between
  // servers (alias/root-relative vs absolute); compare only the identity hash.
  return events.map(e => {
    if (e.type === 'firmware.loaded') { const { filename, ...rest } = e; return rest; }
    return e;
  });
}
let failures = 0;
function compare(label, a, b) {
  const sa = JSON.stringify(a), sb = JSON.stringify(b);
  if (sa !== sb) {
    failures++;
    console.log(`MISMATCH ${label}`);
    for (let i = 0; i < Math.max(sa.length, sb.length); i++) {
      if (sa[i] !== sb[i]) {
        console.log(`  a: ...${sa.slice(Math.max(0, i - 80), i + 120)}`);
        console.log(`  b: ...${sb.slice(Math.max(0, i - 80), i + 120)}`);
        break;
      }
    }
  } else console.log(`ok ${label} (${sa.length}B)`);
}
const cards = async () => compare('cards', (await get(A, '/api/cards')).data, (await get(B, '/api/cards')).data);

// Load the same firmware image on both sides (per-side path syntax).
const loads = [];
for (const [base, image] of [[A, IMAGE_A], [B, IMAGE_B]]) {
  const load = await post(base, '/api/load', { path: image, board: BOARD });
  if (load.status !== 200) throw new Error(`load failed on ${base}: ${JSON.stringify(load.data)}`);
  loads.push(load.data);
}
compare('load response', normalize(loads[0]), normalize(loads[1]));
const boardsA = await get(A, '/api/boards'), boardsB = await get(B, '/api/boards');
compare('boards', boardsA.data, boardsB.data);
const st0 = await get(B, '/api/state');
const boardProfile = boardsA.data.find(b => b.id === st0.data.board.id);
const ramStart = boardProfile.ramStart;
console.log(`image=${IMAGE_A} / ${IMAGE_B} board=${st0.data.board.id} ramStart=0x${ramStart.toString(16)}`);
await cards();

let prev = 0;
for (const target of STEPS) {
  const n = target - prev; prev = target;
  const ra = await post(A, '/api/run', { instructions: n });
  const rb = await post(B, '/api/run', { instructions: n });
  compare(`run->${target} execution`, ra.data.execution, rb.data.execution);
  const sa = await get(A, '/api/state'), sb = await get(B, '/api/state');
  compare(`run->${target} state`, normalize(sa.data), normalize(sb.data));
  const ma = await get(A, `/api/memory?space=physical&address=${ramStart}&length=65536`);
  const mb = await get(B, `/api/memory?space=physical&address=${ramStart}&length=65536`);
  compare(`run->${target} ram`, ma.data.hex, mb.data.hex);
  const la = await get(A, '/api/memory?space=logical&address=0&length=65536');
  const lb = await get(B, '/api/memory?space=logical&address=0&length=65536');
  compare(`run->${target} logical`, la.data.hex, lb.data.hex);
  await cards();
  const ea = await get(A, '/api/events?since=0'), eb = await get(B, '/api/events?since=0');
  compare(`run->${target} events`, normalizeEvents(ea.data), normalizeEvents(eb.data));
  const da = await get(A, '/api/disassembly?count=12'), db = await get(B, '/api/disassembly?count=12');
  compare(`run->${target} disasm`, da.data, db.data);
  if (ra.data.execution.reason !== 'budget') { console.log(`stopped early: ${ra.data.execution.reason}`); break; }
}

// Device inputs exercising peripherals, then compare again.
const inputs = [
  { type: 'handset', data: { offHook: true } },
  { type: 'key', data: { key: '5' } },
  { type: 'sensor', data: { coverOpen: true } },
  { type: 'coin', data: { index: 0 } },
  { type: 'card', data: { action: 'insert', kind: 'magnetic', track2: ';4012888888=3012101000000000000?' } },
];
for (const base of [A, B]) {
  for (const ev of inputs) {
    const r = await post(base, '/api/devices', ev);
    if (r.status !== 200) console.log(`  input ${ev.type} on ${base === A ? 'a' : 'b'}: ${r.status} ${JSON.stringify(r.data.error ?? '')}`);
  }
  await post(base, '/api/run', { instructions: 100000 });
  await post(base, '/api/devices', { type: 'card', data: { action: 'remove' } });
  await post(base, '/api/devices', { type: 'handset', data: { offHook: false } });
  await post(base, '/api/run', { instructions: 100000 });
  await post(base, '/api/devices', { type: 'card', data: { action: 'position', position: 'out' } });
  const si = await post(base, '/api/devices', { type: 'card', data: { action: 'insert', kind: 'smart', hex: '0102030405060708aabbccddee' } });
  if (si.status !== 200) console.log(`  smart insert on ${base === A ? 'a' : 'b'}: ${si.status} ${JSON.stringify(si.data.error ?? '')}`);
  await post(base, '/api/run', { instructions: 200000 });
  await post(base, '/api/devices', { type: 'card', data: { action: 'remove' } });
  await post(base, '/api/run', { instructions: 50000 });
}
const sa2 = await get(A, '/api/state'), sb2 = await get(B, '/api/state');
compare('post-input state', normalize(sa2.data), normalize(sb2.data));
const ea2 = await get(A, '/api/events?since=0'), eb2 = await get(B, '/api/events?since=0');
compare('post-input events', normalizeEvents(ea2.data), normalizeEvents(eb2.data));
await cards();

// Snapshot cross-restore both directions, then identical execution.
const snapA = await get(A, '/api/snapshot');
const imp = await post(B, '/api/snapshot', snapA.data);
if (imp.status !== 200) { failures++; console.log('b restore of a snapshot FAILED', imp.data); }
else {
  console.log('b restore of a snapshot: 200');
  await cards();
  const ra3 = await post(A, '/api/run', { instructions: 100000 });
  const rb3 = await post(B, '/api/run', { instructions: 100000 });
  compare('post-restore execution', ra3.data.execution, rb3.data.execution);
  const sa3 = await get(A, '/api/state'), sb3 = await get(B, '/api/state');
  compare('post-restore state', normalize(sa3.data, true), normalize(sb3.data, true));
  await cards();
}
const snapB = await get(B, '/api/snapshot');
const imp2 = await post(A, '/api/snapshot', snapB.data);
if (imp2.status !== 200) { failures++; console.log('a restore of b snapshot FAILED', imp2.data); }
else {
  console.log('a restore of b snapshot: 200');
  const sa4 = await get(A, '/api/state'), sb4 = await get(B, '/api/state');
  compare('both-restored state', normalize(sa4.data, true), normalize(sb4.data, true));
  await cards();
}

console.log(failures ? `FAILURES: ${failures}` : 'ALL MATCH');
process.exit(failures ? 1 : 0);
