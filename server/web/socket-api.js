// One persistent connection carries live updates and correlated API calls.
// Requests interrupted by disconnect are rejected, never replayed: replaying
// an input/patch could apply a mutation twice.
export function createSocketAPI({url,onState=()=>{},onDisplay=()=>{},onEvents=()=>{},onDebugger=()=>{},onConnection=()=>{}}={}){
  url??=(location.protocol==='https:'?'wss:':'ws:')+'//'+location.host+'/ws';
  let socket,stopped=false,nextId=0,retry=250,reconnectTimer;const pending=new Map(),waiting=new Set();let subscription={visible:true,debugger:true};
  function connect(){
    if(stopped)return;socket=new WebSocket(url);
    socket.addEventListener('open',()=>{retry=250;socket.send(JSON.stringify({type:'subscribe',...subscription}));onConnection(true);for(const waiter of waiting){clearTimeout(waiter.timer);waiter.resolve();}waiting.clear();});
    socket.addEventListener('message',event=>{
      let message;try{message=JSON.parse(event.data);}catch{return;}
      if(message.type==='response'){
        const request=pending.get(message.id);if(!request)return;pending.delete(message.id);clearTimeout(request.timer);
        if(message.status>=400)request.reject(new Error(message.data?.error??'WebSocket request failed'));else request.resolve(message.data);
      }else if(message.type==='state')onState(message.data);
      else if(message.type==='display')onDisplay(message.data);
      else if(message.type==='events')onEvents(message.data);
      else if(message.type==='debugger')onDebugger(message.data);
    });
    socket.addEventListener('close',()=>{
      onConnection(false);for(const request of pending.values()){clearTimeout(request.timer);request.reject(new Error('WebSocket disconnected; command was not retried'));}pending.clear();
      if(!stopped){reconnectTimer=setTimeout(connect,retry);retry=Math.min(retry*2,8000);}
    });
    socket.addEventListener('error',()=>{});
  }
  function ready(){if(stopped)return Promise.reject(new Error('WebSocket closed'));if(socket?.readyState===WebSocket.OPEN)return Promise.resolve();return new Promise((resolve,reject)=>{const waiter={resolve,reject,timer:setTimeout(()=>{waiting.delete(waiter);reject(new Error('WebSocket connection timed out'));},10000)};waiting.add(waiter);});}
  async function request(route,body){await ready();return new Promise((resolve,reject)=>{
    const id=++nextId,timer=setTimeout(()=>{pending.delete(id);reject(new Error('WebSocket request timed out'));},30000);pending.set(id,{resolve,reject,timer});
    try{socket.send(JSON.stringify({type:'request',id,route,method:body===undefined?'GET':'POST',...(body===undefined?{}:{body})}));}catch(e){pending.delete(id);clearTimeout(timer);reject(e);}
  });}
  function subscribe(options){subscription={...subscription,...options};if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify({type:'subscribe',...subscription}));}
  function close(){stopped=true;clearTimeout(reconnectTimer);socket?.close();for(const waiter of waiting){clearTimeout(waiter.timer);waiter.reject(new Error('WebSocket closed'));}waiting.clear();}
  connect();return {request,subscribe,close,ready};
}
