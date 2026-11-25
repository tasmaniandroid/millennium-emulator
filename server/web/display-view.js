// HTTP polling can finish after a newer SSE display update. Sequence the
// actual screen independently so a late poll cannot scroll it backwards.
export function createDisplayRenderer(element) {
  let session=null,sequence=-1;
  const retired=new Set();
  return data=>{
    if(!Array.isArray(data.display)||data.display.length!==2)return false;
    const next=data.view;
    if(next){
      if(next.session!==session){
        if(retired.has(next.session))return false;
        if(session!==null)retired.add(session);
        session=next.session;sequence=-1;
      }
      if(next.sequence<sequence)return false;
      sequence=next.sequence;
    }else if(session!==null)return false;
    element.textContent=data.display.join('\n');
    if(element.dataset){
      element.dataset.mode=String(data.displayHardware?.mode??'');
      element.dataset.cursor=String(data.displayHardware?.cursor??'');
    }
    return true;
  };
}
