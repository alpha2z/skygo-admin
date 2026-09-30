/* Publication intent is persisted on the server. Browser bookmarks only hold IDs. */
'use strict';
const PublicationUI = (() => {
 let busy=false, stopWatching=()=>{};
 // Only read the existing publication. Never resubmit after a lost connection.
 function watch({api,id,active,onRecord,onError,schedule=setTimeout,cancel=clearTimeout,limit=60}){
  let stopped=false,timer,attempts=0;
  const stop=()=>{stopped=true;cancel(timer);};
  const tick=async()=>{
   if(stopped||!active())return;
   attempts++;
   try{const p=await api('publications/'+encodeURIComponent(id));if(stopped||!active())return;onRecord(p);
    if(!['pending','queued','dispatched'].includes(p.status)){stop();return;}
   }catch(e){if(stopped||!active())return;onError(e);if(e.status===401||e.status===403){stop();return;}}
   if(attempts<limit)timer=schedule(tick,5000);else onError(Error('Automatic status checks paused. Use Refresh to query the same update.'));
  };
  timer=schedule(tick,5000);return stop;
 }
 function dispose(){stopWatching();stopWatching=()=>{};}

 function state(hash){const p=new URLSearchParams(hash.startsWith('#system-update?')?hash.slice(15):hash.startsWith('#publication?')?hash.slice(13):'');return {host:p.get('host')||'',release:p.get('release')||'',selected:p.has('selected')?p.get('selected').split(',').filter(Boolean):null,request:p.get('request')||''};}
 function locationFor(s,systemUpdate=false){const p=new URLSearchParams();for(const k of ['host','release','request'])if(s[k])p.set(k,s[k]);if(s.selected!==null)p.set('selected',s.selected.join(','));return (systemUpdate?'#system-update?':'#publication?')+p;}
 function selection(targets,selected){const eligible=targets.filter(t=>t.available&&t.changed).map(t=>t.service);return selected===null?eligible:selected.filter(id=>eligible.includes(id));}
 function canSubmit(targets,selected){return selected.length>0&&selected.every(id=>targets.some(t=>t.service===id&&t.changed&&t.available&&t.preparation==='ready'));}
 function node(tag,text){const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n;}
 async function render(ctx){
  dispose();
  const {root,api,allowed,notice,isCurrent,refresh,sessionID,systemUpdate=false}=ctx;
  const s=state(location.hash),panel=node('section');root.replaceChildren(panel);
  const update=()=>history.replaceState(null,'',locationFor(s,systemUpdate));
  const intentChanged=()=>{s.request='';update();refresh();};
  function button(text,action,disabled=false){const b=node('button',text);b.type='button';b.disabled=disabled||busy;b.onclick=async()=>{if(busy)return;busy=true;b.disabled=true;try{await action();}catch(e){notice(e.message);}finally{busy=false;if(isCurrent())await refresh();}};return b;}
  if(systemUpdate){
   panel.append(node('h2','Update this management installation'),node('p','Choose the enrolled host that runs this API/Web pair. Agent executes the signed update independently while the management service is offline.'),node('p','Agent upgrades and database schema changes require a separate maintenance procedure. Verify release compatibility before submitting.'));
   panel.append(button('Check versions and status',async()=>{}));
  }
  if(!allowed('ops.read')||!allowed('build.read')){panel.append(node('p','Requires ops.read and build.read permissions.'));return;}
  const records=await api('publications');if(!isCurrent())return;
  const historyPanel=node('details');historyPanel.append(node('summary','Publication history'));
  const showRecord=p=>{
   const card=node('article');card.append(node('h3',p.id+' · '+p.status),node('p','Host '+p.host_id+' · version '+p.release_id),node('p','Execution '+p.execution_id));
   let scope;try{scope=JSON.parse(p.scope);}catch{card.append(node('p','Invalid scope: actions disabled.'));return card;}
   for(const t of scope.unit.targets)card.append(node('p',t.service+': '+(t.selected?'replace image':'keep original image; restart and health-check')+' → '+t.image));
   if(p.code)card.append(node('p',p.code));
   if(p.status==='uncertain')card.append(node('p','Result uncertain: locks are retained. Inspect the Agent journal and actual containers before recovery.'));
   if(p.status==='pending'&&allowed('ops.approve')){
    card.append(button('Approve publication',async()=>{await api('publications/'+encodeURIComponent(p.id)+'/approve','POST');},p.requested_by===sessionID));
    card.append(button('Reject publication',async()=>{await api('publications/'+encodeURIComponent(p.id)+'/reject','POST');}));
   }
   return card;
  };
  if(s.request){try{const p=await api('publications/'+encodeURIComponent(s.request));if(!isCurrent())return;const currentRecord=node('section');currentRecord.append(showRecord(p));panel.append(currentRecord);
   if(['pending','queued','dispatched'].includes(p.status)){
    const connection=node('p','Checking update status without changing your selection.');panel.append(connection);
    stopWatching=watch({api,id:p.id,active:isCurrent,onRecord:next=>{currentRecord.replaceChildren(showRecord(next));connection.textContent=next.status==='uncertain'?'Result uncertain. Locks are retained; inspect the Agent journal before recovery.':['pending','queued','dispatched'].includes(next.status)?'Update in progress.':'Update finished. Check versions and status to reload the running inventory.';},onError:e=>{connection.textContent=e.status===401?'Session expired. Reload and sign in again to resume this update.':e.status===403?'Status access denied.':e.message.startsWith('Automatic')?e.message:'Management service temporarily unavailable; reconnecting to the same update.';}});
   }
  }catch(e){if(e.status!==404)throw e;panel.append(node('p','Request '+s.request+' has no confirmed publication yet. Retry submission with this same request ID.'));}}
  for(const p of records){const row=node('p');row.append(button(p.id+' · '+p.status,async()=>{s.request=p.id;s.host=p.host_id;s.release=p.release_id;try{s.selected=JSON.parse(p.selection);}catch{s.selected=[];}update();}));historyPanel.append(row);}
  panel.append(historyPanel);
  const releases=await api('releases');if(!isCurrent())return;
  const version=node('select');version.append(new Option('Choose trusted version',''));for(const r of releases)version.append(new Option(DistributionUI.buildTime(r.build_started_at)+' · '+r.id+' · '+r.manifest.build.ref+' · '+r.manifest.build.source_commit.slice(0,12),r.id));
  if(s.release&&!releases.some(r=>r.id===s.release)){const old=await api('releases/'+encodeURIComponent(s.release));version.append(new Option(old.id,old.id));}
  version.value=s.release;version.onchange=()=>{s.release=version.value;s.selected=null;intentChanged();};const versionLabel=node('label','Trusted version');versionLabel.append(version);panel.append(versionLabel);
  if(!s.release)panel.append(node('p','Register a signed build in Versions & upgrades, then select it here. Current inventory is shown below; preparation requires a trusted version.'));
  const data=await api('publication-candidates'+(s.release?'?release_id='+encodeURIComponent(s.release):''));if(!isCurrent())return;
  panel.append(node('p','Registered hosts: '+data.host_count));
  const hostSelect=node('select');hostSelect.append(new Option('Choose host',''));for(const h of data.hosts)hostSelect.append(new Option(h.host_id+(h.reason?' · '+h.reason:''),h.host_id));
  if(s.host&&!data.hosts.some(h=>h.host_id===s.host)){hostSelect.append(new Option(s.host+' · unavailable',s.host));}
  hostSelect.value=s.host;hostSelect.onchange=()=>{s.host=hostSelect.value;s.selected=null;intentChanged();};const hostLabel=node('label','Target host');hostLabel.append(hostSelect);panel.append(hostLabel);
  const h=data.hosts.find(h=>h.host_id===s.host);
  if(!h){panel.append(node('p',data.host_count?'Choose a host explicitly. An unavailable previous target is never replaced automatically.':'No hosts enrolled.'));return;}
  if(h.reason){panel.append(node('p',h.reason));return;}
  s.selected=selection(h.targets,s.selected);update();
  for(const t of h.targets){
   const card=node('article'),label=node('label'),check=node('input');check.type='checkbox';check.checked=s.selected.includes(t.service);check.disabled=!t.available||!t.changed||busy;check.onchange=()=>{s.selected=check.checked?[...s.selected,t.service]:s.selected.filter(x=>x!==t.service);intentChanged();};label.append(check,document.createTextNode('Replace '+t.service+' ('+t.kind+')'));card.append(label);
   card.append(node('p','Current image: '+t.current_image_id));const p=t.provenance;
   card.append(node('p',p.status==='matched'?'Version '+p.release_id+' · Source: '+p.ref+' · '+p.commit.slice(0,12)+' · built '+DistributionUI.buildTime(p.build_started_at):'Source '+p.status+' (no tag-based inference)'));
   card.append(node('p',t.available?(t.changed?'Target '+t.image:'Already runs target image'):s.release?'This version has no matching image; retain current image.':'Select a trusted version to compare images.'));
   card.append(node('p','Preparation: '+t.preparation+(t.task_id?' · task '+t.task_id:'')));
   if(!check.checked)card.append(node('p','Keeps its current image and participates in stop, restart, health checks and rollback.'));
   panel.append(card);
  }
  if(!s.release)return;
  function request(){if(!s.request){s.request='pub-'+crypto.randomUUID();update();}return {request_id:s.request,host_id:s.host,release_id:s.release,selected:s.selected};}
  panel.append(button('Sync selected images',async()=>{await api('publication-preparation','POST',request());notice('Image preparation requested. Containers are unchanged. Refresh for receipts.');},!allowed('ops.write')||!s.selected.length));
  panel.append(button('Submit for independent approval',async()=>{const p=await api('publications','POST',request());s.request=p.id;update();notice('Publication saved. A different administrator must approve it.');},!allowed('ops.write')||!canSubmit(h.targets,s.selected)));
  if(!s.selected.length)panel.append(node('p','No image changes selected; no publication will be created.'));
 }
 return {state,locationFor,selection,canSubmit,render,watch,dispose,get busy(){return busy;}};
})();
if(typeof module!=='undefined')module.exports=PublicationUI;
