// NCC TCP bridge differential test: drives identical modem-relay/ASCI1
// sequences against both servers with a shared TCP echo target, then
// compares transport state and event logs.
import net from 'node:net';

const args = process.argv.slice(2);
const usage = 'Usage: node ncccheck.mjs <portA> <portB> <imageA> <imageB>';
if (args.length === 1 && args[0] === '--help') {
  console.log(usage);
  process.exit(0);
}
const [portA, portB, imageA, imageB] = args;
const validPort = port => /^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535;
if (args.length !== 4 || !validPort(portA) || !validPort(portB) || !imageA || !imageB) {
  console.error(usage);
  process.exit(2);
}
const A = `http://127.0.0.1:${Number(portA)}`;
const B = `http://127.0.0.1:${Number(portB)}`;

const echo = net.createServer(sock => { sock.pipe(sock); });
await new Promise(r => echo.listen(0, '127.0.0.1', r));
const echoPort = echo.address().port;

async function post(base, route, body) {
  const res = await fetch(base + route, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body ?? {}) });
  return { status: res.status, data: await res.json() };
}
async function get(base, route) {
  const res = await fetch(base + route);
  return { status: res.status, data: await res.json() };
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

async function drive(base, label, image) {
  const loaded = await post(base, '/api/load', { path: image, board: 'auto' });
  if (loaded.status !== 200) {
    throw new Error(`${label} could not load ${JSON.stringify(image)}: HTTP ${loaded.status} ${JSON.stringify(loaded.data)}`);
  }
  await post(base, '/api/reset', { cold: true });
  const cfg = await post(base, '/api/ncc', { mode: 'connect', host: '127.0.0.1', port: echoPort, autoHandshake: false, receiveDelayMs: 100 });
  if (cfg.status !== 200) throw new Error(`${label} ncc config: ${JSON.stringify(cfg.data)}`);
  // Seize the modem relay: ...10 (released) -> ...11 (new call).
  await post(base, '/api/ports', { port: 0x60, value: 2 });
  await post(base, '/api/ports', { port: 0x60, value: 3 });
  // Firmware-side ASCI1 transmissions open the TCP session.
  for (const b of [0x11, 0x22, 0x33, 0x44]) {
    await post(base, '/api/ports', { port: 0x07, value: b });
  }
  // Let connect + echo + pumps settle, then run so rx becomes deliverable.
  await sleep(800);
  await post(base, '/api/run', { instructions: 200000 });
  await sleep(800);
  await post(base, '/api/run', { instructions: 200000 });
  await sleep(800);
  const state = await get(base, '/api/ncc');
  const events = await get(base, '/api/events?since=0');
  const nccEvents = events.data.filter(e => e.type.startsWith('ncc.') || e.type.startsWith('modem.'));
  // Release the relay: ends the call-scoped session deterministically.
  await post(base, '/api/ports', { port: 0x60, value: 2 });
  await sleep(1500);
  const after = await get(base, '/api/ncc');
  const events2 = await get(base, '/api/events?since=0');
  const nccEvents2 = events2.data.filter(e => e.type.startsWith('ncc.') || e.type.startsWith('modem.'));
  return { state: state.data, nccEvents, after: after.data, nccEvents2 };
}

const a = await drive(A, 'server A', imageA);
const b = await drive(B, 'server B', imageB);

// TCP segmentation and rx/tx interleaving carry no significance (per the
// bridge contract and OS socket timing), so aggregate totals per direction
// and keep only the deterministic lifecycle/modem events in order.
function normalizeEvents(events) {
  const out = [];
  let tx = { bytes: 0, hex: '' }, rx = { bytes: 0, hex: '' };
  for (const e of events) {
    const { id, cycle, ...rest } = e;
    if (rest.type === 'ncc.tcp.tx') { tx.bytes += rest.bytes; tx.hex += rest.hex; continue; }
    if (rest.type === 'ncc.tcp.rx') { rx.bytes += rest.bytes; rx.hex += rest.hex; continue; }
    out.push(rest);
  }
  if (tx.bytes) out.push({ type: 'ncc.tcp.tx-total', ...tx });
  if (rx.bytes) out.push({ type: 'ncc.tcp.rx-total', ...rx });
  return out;
}

let failures = 0;
function compare(label, x, y) {
  const sx = JSON.stringify(x), sy = JSON.stringify(y);
  if (sx !== sy) {
    failures++;
    console.log(`MISMATCH ${label}`);
    console.log(`  A: ${sx.slice(0, 600)}`);
    console.log(`  B: ${sy.slice(0, 600)}`);
  } else console.log(`ok ${label} (${sx.length}B)`);
}

compare('ncc state (mid-call)', a.state, b.state);
compare('ncc/modem events (mid-call)', normalizeEvents(a.nccEvents), normalizeEvents(b.nccEvents));
compare('ncc state (after hangup)', a.after, b.after);
compare('ncc/modem events (after hangup)', normalizeEvents(a.nccEvents2), normalizeEvents(b.nccEvents2));

echo.close();
console.log(failures ? `FAILURES: ${failures}` : 'ALL MATCH');
process.exit(failures ? 1 : 0);
