import { createDisplayRenderer } from './display-view.js';
import { createSocketAPI } from './socket-api.js';
const $=id=>document.getElementById(id),hex=(v,n=4)=>'0x'+v.toString(16).padStart(n,'0');let state=null,eventId=0,refreshing=false,displayedFirmware=null;
const renderDisplay=createDisplayRenderer($('display'));
const flashOption=document.createElement('option');flashOption.value='flash';flashOption.textContent='flash image';$('memory-space').append(flashOption);
const coinBasicBoard=document.createElement('option');coinBasicBoard.value='mtr1-coin-basic';coinBasicBoard.textContent='MTR1 Coin Basic';$('board').append(coinBasicBoard);
try{const style=localStorage.getItem('millennium.phoneStyle');if(['auto','wall','desk'].includes(style))$('phone-style').value=style;}catch{}
function phoneAppearance(s){
  const selected=$('phone-style').value,name=s?.firmware?.path?.split(/[\\/]/).at(-1)??'';
  const variant=selected==='auto'?(/^N[AB]J/i.test(name)?'desk':'wall'):selected;
  document.querySelector('.phone').dataset.variant=variant;document.querySelector('.phone-stage').dataset.variant=variant;
  $('phone-model').textContent=variant==='desk'?'Millennium Desk Terminal':'Millennium MultiPay';
}
$('phone-style').addEventListener('change',()=>{try{localStorage.setItem('millennium.phoneStyle',$('phone-style').value);}catch{}phoneAppearance(state);});

const tabs = [...document.querySelectorAll('.inspector-tabs [role="tab"]')];
function selectTab(active) {
  for (const tab of tabs) {
    const selected = tab === active;
    tab.setAttribute('aria-selected', String(selected));
    tab.tabIndex = selected ? 0 : -1;
    $(tab.getAttribute('aria-controls')).hidden = !selected;
  }
}
for (const [index, tab] of tabs.entries()) {
  tab.addEventListener('click', () => selectTab(tab));
  tab.addEventListener('keydown', event => {
    const next = { ArrowLeft: (index + tabs.length - 1) % tabs.length,
      ArrowRight: (index + 1) % tabs.length, Home: 0, End: tabs.length - 1 }[event.key];
    if (typeof next !== 'number') return;
    event.preventDefault();
    selectTab(tabs[next]);
    tabs[next].focus();
  });
}

function error(e){$('error').hidden=false;$('error').textContent=e.message??String(e);}
let initialized=false,latestState=null;
const transport=createSocketAPI({
  onState:s=>{latestState=s;if(!initialized)return;if(state&&(s.eventId<state.eventId||s.view?.session!==state.view?.session)){eventId=0;$('events').textContent='';}render(s);},
  onDisplay:s=>{if(initialized)renderDisplay(s);},
  onEvents:data=>{if(!initialized)return;if(data.reset){eventId=0;$('events').textContent='';}appendEvents(data.rows);},
  onDebugger:data=>{if(initialized)renderDisassembly(data.rows);},
  onConnection:connected=>{
    $('connection').dataset.connected = String(connected);
    $('connection').textContent = connected ? 'Connected' : 'Reconnecting…';
  },
});
async function api(route,body){return transport.request(route,body);}
function action(id,fn){$(id).addEventListener('click',async()=>{try{$('error').hidden=true;await fn();await refresh();}catch(e){error(e);}});}
function updateRunControls() {
  const loaded = !!state?.firmware, running = !!state?.running;
  for (const id of ['resume','step','advance','reset','add-break','clear-break','apply-patch','revert-patch','save-snapshot','restore-snapshot','download-snapshot']) {
    $(id).disabled = !loaded || running;
  }
  $('pause').disabled = !running;
  $('load').disabled = running || !$('firmware').value;
  $('upload-firmware').disabled = running;
  const selection = $('firmware').value.replaceAll('\\', '/');
  let primary = 'attach-fw';
  if (running) primary = 'pause';
  else if (selection && selection !== displayedFirmware?.replaceAll('\\', '/')) primary = 'load';
  else if (loaded) primary = 'resume';
  for (const id of ['attach-fw','load','resume','pause']) $(id).classList.toggle('primary', id === primary);
  $('firmware').title = $('firmware').value;
}

function render(s){if(!renderDisplay(s))return;state=s;
  const mode = !s.firmware ? 'idle' : s.running ? 'running' : s.fault ? 'fault' : 'paused';
  const status = {idle:'No ROM loaded', running:'Running', fault:`Fault: ${s.fault?.message}`, paused:'Paused'}[mode];
  const detail = mode === 'paused' && ['breakpoint', 'watchpoint'].includes(s.stopReason) ? ` · ${s.stopReason}` : '';
  if ($('status').textContent !== status + detail) $('status').textContent = status + detail;
  $('status').dataset.mode = mode;
  $('time').textContent=`${(s.milliseconds/1000).toFixed(3)} s simulated · ${s.cpu.instructions.toLocaleString()} instructions`+(s.running&&s.realtime?` · ${s.realtime.achievedFactor.toFixed(2)}× actual / ${s.realtime.factor}× target`:'');
  phoneAppearance(s);
  if(s.nccState)$('ncc-state').textContent=JSON.stringify(s.nccState,null,2);
  if(s.patchManifest)$('patches').textContent=JSON.stringify(s.patchManifest,null,2);
  if(s.realtime&&document.activeElement!==$('speed'))$('speed').value=String(s.realtime.factor);
  if(s.firmware&&s.firmware.path!==displayedFirmware){
    const match=[...$('firmware').options].find(o=>o.value.replaceAll('\\','/')===s.firmware.path.replaceAll('\\','/'));
    if(match)$('firmware').value=match.value;
    displayedFirmware=s.firmware.path;
  }
  $('registers').replaceChildren();const regs={PC:s.cpu.pc,SP:s.cpu.sp,AF:s.cpu.a<<8|s.cpu.f,BC:s.cpu.b<<8|s.cpu.c,DE:s.cpu.d<<8|s.cpu.e,HL:s.cpu.h<<8|s.cpu.l,IX:s.cpu.ix,IY:s.cpu.iy,CBR:s.mmu.cbr,BBR:s.mmu.bbr,CBAR:s.mmu.cbar};for(const [k,v]of Object.entries(regs)){const d=document.createElement('div');d.className='register';const label=document.createElement('span'),b=document.createElement('b');label.textContent=k;b.textContent=v.toString(16).toUpperCase().padStart(k.length>2?2:4,'0');d.append(label,b);$('registers').append(d);}
  $('rom-info').textContent=s.firmware?`${s.board?.id??'mtr1'} · ${s.firmware.size.toLocaleString()} bytes · Physical PC ${hex(s.physicalPC,5)} · IFF ${s.cpu.iff1?1:0} · IM ${s.cpu.im}\n${s.firmware.path}\nSHA-256 ${s.firmware.sha256}`:'';
  const handsetLabel=s.devices.offHook?'↙ Replace handset':'↗ Lift handset';$('handset-label').textContent=handsetLabel;$('handset').setAttribute('aria-label',handsetLabel);$('handset').setAttribute('aria-pressed',String(s.devices.offHook));$('handset').title=handsetLabel;$('handset').classList.toggle('active',s.devices.offHook);
  document.querySelector('.phone').dataset.cardPosition=s.devices.reader.position;
  const cardLabel=s.devices.reader.position==='out'?'Insert magnetic card':'Remove card';$('face-card-label').textContent=cardLabel;$('face-card').setAttribute('aria-label',cardLabel);$('face-card').title=cardLabel;
  for(const k of ['lineConnected','reversed','ring','coverOpen','cashboxRemoved'])$(k).checked=s.devices[k];
  for(const k of ['dialTone','answerTone','carrier'])$(k).checked=s.devices.modem[k];
  $('card-state').textContent=`${s.devices.reader.kind??'No card'} · ${s.devices.reader.position}\nDMA samples: ${s.devices.reader.dmaSamples} · Smart reads: ${s.devices.reader.reads}\nCounter writes: ${s.devices.reader.writes}`;
  $('device-state').textContent=`Dialed: ${s.devices.dialed||'—'}\nRelays: ${s.devices.relays.toString(2).padStart(3,'0')} · Call state: ${s.devices.callState}\nEscrow: ${s.devices.coinEscrow.length} coin(s)\nCollected: ${s.devices.coinCollected} · Returned: ${s.devices.coinReturned}`;
  $('breakpoints').textContent=s.breakpoints.length?JSON.stringify(s.breakpoints,null,2):'No breakpoints';$('capabilities').replaceChildren();for(const [k,v]of Object.entries(s.capabilities)){const dt=document.createElement('dt'),dd=document.createElement('dd');dt.textContent=k;dd.textContent=v;$('capabilities').append(dt,dd);}
  updateRunControls();
}
function renderDisassembly(rows){$('disassembly').replaceChildren();for(const r of rows){const tr=document.createElement('tr');if(r.address===state?.cpu.pc)tr.className='current';for(const value of [hex(r.address),hex(r.physical,5),r.bytes,r.text+(r.symbols.length?' ; '+r.symbols.join(', '):'')]){const td=document.createElement('td');td.textContent=value;tr.append(td);}$('disassembly').append(tr);}}
async function disassembly(){const value=$('disasm-address').value.trim();transport.subscribe({address:value||null});renderDisassembly(await api(`disassembly?address=${value||state?.cpu.pc||0}&count=24`));}
function appendEvents(input){const rows=input.filter(e=>e.id>eventId);if(rows.length){eventId=rows.at(-1).id;const lines=rows.map(e=>`${String(e.cycle).padStart(10)}  ${e.type}  ${JSON.stringify(Object.fromEntries(Object.entries(e).filter(([k])=>!['cycle','id','type'].includes(k))))}`).join('\n');$('events').textContent=($('events').textContent+'\n'+lines).split('\n').slice(-150).join('\n');$('events').scrollTop=$('events').scrollHeight;}}
async function events(){appendEvents(await api('events?since='+eventId));}
async function refresh(){if(refreshing)return;refreshing=true;try{render(await api('state'));await Promise.all([disassembly(),events()]);$('patches').textContent=JSON.stringify(await api('patches'),null,2);$('ncc-state').textContent=JSON.stringify(await api('ncc'),null,2);}finally{refreshing=false;}}
async function device(type,data){await api('devices',{type,data});}
for(const [i,key]of [...'123456789*0#'].entries()){const button=document.createElement('button');button.textContent=key;const small=document.createElement('small');small.textContent=['','ABC','DEF','GHI','JKL','MNO','PQRS','TUV','WXYZ','','OPERATOR',''][i];button.append(small);button.addEventListener('click',()=>device('key',{key}).catch(error));$('keypad').append(button);}
document.querySelectorAll('[data-key]').forEach(button=>button.addEventListener('click',()=>device('key',{key:button.dataset.key}).catch(error)));
for(let i=0;i<16;i++){const o=document.createElement('option');o.value=i;o.textContent=`Type ${i+1}`;$('coin-type').append(o);}
for(const k of ['lineConnected','reversed','ring','coverOpen','cashboxRemoved'])$(k).addEventListener('change',()=>device(['coverOpen','cashboxRemoved'].includes(k)?'sensor':'line',{[k]:$(k).checked}).catch(error));
for(const k of ['dialTone','answerTone','carrier'])$(k).addEventListener('change',()=>device('line',{[k]:$(k).checked}).catch(error));
action('answer-call',()=>device('answer',{delayMs:Number($('answer-delay').value),method:'both'}));
action('insert-magnetic',()=>device('card',{action:'insert',kind:'magnetic',track2:$('track2').value}));
action('remove-card',()=>device('card',{action:'remove'}));
action('insert-smart',()=>device('card',{action:'insert',kind:'smart',hex:$('smart-image').value,writeProtected:$('smart-readonly').checked}));
function downloadJSON(data,name){const url=URL.createObjectURL(new Blob([JSON.stringify(data,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
action('export-card',async()=>downloadJSON(await api('cards'),'millennium-card.json'));
$('smart-file').addEventListener('change',async e=>{try{const file=e.target.files[0];if(file){if(file.size<13||file.size>32)throw new Error('Smart card dump must be 13..32 bytes');$('smart-image').value=[...new Uint8Array(await file.arrayBuffer())].map(b=>b.toString(16).padStart(2,'0')).join('');}}catch(e){error(e);}finally{e.target.value='';}});
action('configure-ncc',()=>api('ncc',{mode:$('ncc-mode').value,host:$('ncc-host').value,port:Number($('ncc-port').value),autoHandshake:$('ncc-handshake').checked,receiveDelayMs:Number($('ncc-latency').value)}));
$('speed').addEventListener('change',()=>api('speed',{factor:Number($('speed').value)}).then(refresh).catch(error));
action('load',async()=>{await api('load',{path:$('firmware').value,board:$('board').value});$('firmware-note').textContent='';eventId=0;$('events').textContent='';});
action('resume',()=>api('resume',{}));action('pause',()=>api('pause',{}));action('step',()=>api('step',{}));action('advance',()=>api('run',{instructions:1000000}));action('reset',async()=>{await api('reset',{});eventId=0;$('events').textContent='';});
action('handset',()=>device('handset',{offHook:!state.devices.offHook}));action('coin',()=>device('coin',{index:Number($('coin-type').value)}));
action('face-coin',()=>device('coin',{index:Number($('coin-type').value)}));
action('face-card',()=>device('card',state?.devices.reader.position==='out'?{action:'insert',kind:'magnetic',track2:$('track2').value}:{action:'remove'}));
action('disassemble',disassembly);action('read-memory',async()=>{const m=await api(`memory?space=${$('memory-space').value}&address=${$('memory-address').value}&length=256`);const b=m.hex.match(/.{2}/g);$('memory').textContent=Array.from({length:16},(_,i)=>`${hex(m.address+i*16,5)}  ${b.slice(i*16,i*16+16).join(' ')}  ${b.slice(i*16,i*16+16).map(v=>{const n=parseInt(v,16);return n>=32&&n<127?String.fromCharCode(n):'.';}).join('')}`).join('\n');});
action('find-symbol',async()=>{const rows=await api('symbols?search='+encodeURIComponent($('symbol-search').value));$('symbols').textContent=rows.slice(0,100).map(s=>`${hex(s.physical,5)}  ${hex(s.address)}  ${s.name} (${s.module})`).join('\n')+(rows.length>100?'\n… Refine search for more results':'');});
action('add-break',()=>api('breakpoints',{address:$('break-address').value,space:'physical'}));action('clear-break',()=>api('breakpoints',{clear:true}));
action('apply-patch',()=>api('patches',{offset:$('patch-offset').value,expected:$('patch-expected').value,hex:$('patch-hex').value,inputSha256:state.firmware.sha256}));action('revert-patch',()=>api('patches/revert',{}));
action('save-snapshot',()=>api('snapshots',{name:$('snapshot-name').value}));action('restore-snapshot',async()=>{await api('snapshots/restore',{name:$('snapshot-name').value});eventId=0;$('events').textContent='';});
action('download-snapshot',async()=>{const data=await api('snapshot');const url=URL.createObjectURL(new Blob([JSON.stringify(data)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='millennium-snapshot.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
$('import-snapshot').addEventListener('change',async e=>{try{const file=e.target.files[0];if(file){await api('snapshot',JSON.parse(await file.text()));eventId=0;$('events').textContent='';await refresh();}}catch(e){error(e);}finally{e.target.value='';}});
$('trace-enabled').addEventListener('change',()=>api('trace',{enabled:$('trace-enabled').checked,limit:2000}).catch(error));
action('inject',()=>{const type=$('injection-type').value;return device(type==='telephony'?'telephony':'serial',{hex:$('injection-hex').value,...(type==='telephony'?{}:{channel:type==='serial0'?0:1})});});action('refresh-events',events);
async function firmwares(preferred) {
  const list = await api('firmwares');
  const select = $('firmware');
  const previous = preferred ?? (select.value || state?.firmware?.path);
  select.replaceChildren();
  const names = list.map(f => f.path.split(/[\\/]/).at(-1));
  const counts = new Map();
  for (const name of names) counts.set(name, (counts.get(name) ?? 0) + 1);
  for (const [i, f] of list.entries()) {
    const name = names[i];
    const option = new Option(counts.get(name) > 1 ? `${name} · ${f.path}` : name, f.path);
    select.append(option);
  }
  if (!list.length) select.add(new Option('No images attached', ''));
  if (list.some(f => f.path === previous)) select.value = previous;
  select.disabled = !list.length;
  updateRunControls();
}
$('firmware').addEventListener('change', updateRunControls);

const attachDialog = $('attach-dialog');
$('attach-fw').addEventListener('click', () => {
  $('attach-form').reset();
  $('attach-error').hidden = true;
  attachDialog.showModal();
});
$('cancel-attach').addEventListener('click', () => attachDialog.close());
attachDialog.addEventListener('cancel', event => {
  if ($('submit-attach').disabled) event.preventDefault();
});
$('attach-form').addEventListener('submit', async event => {
  event.preventDefault();
  const path = $('attach-path').value.trim();
  const notice = $('attach-error');
  notice.hidden = true;
  if (!path) {
    notice.textContent = 'Enter a file or folder path.';
    notice.hidden = false;
    return;
  }
  $('submit-attach').disabled = $('cancel-attach').disabled = true;
  try {
    const result = await api('attach', {path});
    await firmwares(result.images[0]?.path);
    attachDialog.close();
    const count = result.images.length;
    $('firmware-note').textContent = count ? `Attached ${count} ROM image${count === 1 ? '' : 's'}.` : 'Source attached; no .bin files found.';
  } catch (e) {
    notice.textContent = e.message;
    notice.hidden = false;
  } finally {
    $('submit-attach').disabled = $('cancel-attach').disabled = false;
  }
});
$('upload-firmware').addEventListener('change',async e=>{try{$('error').hidden=true;const file=e.target.files[0];if(!file)return;if(file.size<1||file.size>0x100000)throw new Error('ROM must contain 1..1048576 bytes');const image=[...new Uint8Array(await file.arrayBuffer())].map(v=>v.toString(16).padStart(2,'0')).join('');const uploaded=await api('firmwares',{name:file.name,hex:image});await firmwares();$('firmware').value=uploaded.path;await api('load',{path:uploaded.path,board:$('board').value});eventId=0;$('events').textContent='';await refresh();}catch(e){error(e);}finally{e.target.value='';}});
document.addEventListener('visibilitychange',()=>transport.subscribe({visible:document.visibilityState==='visible'}));
window.addEventListener('pagehide',()=>transport.close());
try{await transport.ready();await firmwares();initialized=true;if(latestState)render(latestState);await refresh();}catch(e){error(e);}
