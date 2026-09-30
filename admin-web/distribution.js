'use strict';
const DistributionUI=(()=>{
 function buildTime(value){if(!value||!Number.isFinite(Date.parse(value)))return 'Build time unknown';return new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Tokyo',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hourCycle:'h23'}).format(new Date(value))+' JST';}
 function bytes(value){return value===null||value===undefined?'unknown':Number(value).toLocaleString()+' B';}
 const node=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n;};
 async function render(ctx){const {root,api,allowed,notice,isCurrent,refresh,sessionID}=ctx;root.replaceChildren();let busy=false;
  function button(text,action,disabled){const b=node('button',text);b.type='button';b.disabled=!!disabled;b.onclick=async()=>{if(busy)return;busy=true;b.disabled=true;try{await action();}catch(e){notice(e.message);}finally{busy=false;if(isCurrent())await refresh();}};return b;}
  const bookmark=new URLSearchParams(location.hash.startsWith('#distribution?')?location.hash.slice(14):'');const selected=bookmark.get('delivery')||'';
  const jobs=await api('image-deliveries');if(!isCurrent())return;root.append(node('p','Refresh explicitly to view progress. Tasks continue in the background; refreshing does not retry them.'));
  const jobSelect=node('select');jobSelect.append(new Option('Select delivery',''));for(const job of jobs)jobSelect.append(new Option(job.service+' · '+job.host_id+' · '+job.status+' · '+job.id,job.id));if(selected&&!jobs.some(j=>j.id===selected)){const old=await api('image-deliveries/'+encodeURIComponent(selected));jobSelect.append(new Option(old.id+' · '+old.status,old.id));}jobSelect.value=selected;jobSelect.onchange=()=>{history.replaceState(null,'','#distribution?'+new URLSearchParams({delivery:jobSelect.value}));refresh();};root.append(jobSelect);
  if(selected){const job=await api('image-deliveries/'+encodeURIComponent(selected));const history=await api('image-deliveries/'+encodeURIComponent(selected)+'/attempts');if(!isCurrent())return;root.append(node('h3',job.service+' · '+job.mode+' · '+job.status),node('p',job.image));
   if(!history.recorded)root.append(node('p','Attempt history unknown; legacy data is not reconstructed.'));
   for(const a of history.attempts){const item=node('article');item.append(node('h4','Attempt '+a.number+' · '+a.phase+' · '+a.status),node('p','Started '+a.started_at+' · finished '+(a.finished_at||'pending')),node('p','Processed '+bytes(a.bytes)+' / '+(a.total?bytes(a.total):'unknown')+' · reused '+bytes(a.reused)+' · downloaded '+bytes(a.downloaded)),node('p',a.reason||''));root.append(item);}
   root.append(button('Retry image preparation',()=>api('image-deliveries/'+encodeURIComponent(selected)+'/retry','POST'),!allowed('ops.write')||!['failed','expired','succeeded'].includes(job.status)));
  }
  const cleanup=node('details');cleanup.append(node('summary','Image cleanup — preview and independent approval'));root.append(cleanup);
  const records=await api('image-cleanup');const inventory=await api('image-cleanup/resources');if(!isCurrent())return;
  cleanup.append(node('p','Keep latest '+inventory.keep_versions+' versions and '+inventory.keep_days+' days, plus current, rollback and unresolved-task references. No global prune.'));
  for(const r of inventory.resources){const card=node('article');card.append(node('p',(r.host_id||'Central cache')+' · '+r.kind+' · '+r.image),node('p',r.reason||'Eligible for reviewed cleanup'));let requestID=bookmark.get('resource')===r.id?(bookmark.get('cleanup')||''):'';card.append(button('Create cleanup preview',async()=>{if(!requestID){requestID='cleanup-'+crypto.randomUUID();bookmark.set('resource',r.id);bookmark.set('cleanup',requestID);history.replaceState(null,'','#distribution?'+bookmark);} await api('image-cleanup','POST',{request_id:requestID,resource_id:r.id});},!!r.reason||!allowed('ops.write')));cleanup.append(card);}
  for(const p of records){const card=node('article');card.append(node('h4',p.id+' · '+p.status),node('p',p.code||''));let scope;try{scope=JSON.parse(p.scope);}catch{card.append(node('p','Invalid scope — actions disabled.'));cleanup.append(card);continue;}card.append(node('p',(scope.host_id||'Central cache')+' · '+scope.kind+' · '+scope.image));if(p.status==='pending'&&allowed('ops.approve')){card.append(button('Approve exact cleanup',()=>api('image-cleanup/'+encodeURIComponent(p.id)+'/approve','POST'),p.requested_by===sessionID),button('Reject',()=>api('image-cleanup/'+encodeURIComponent(p.id)+'/reject','POST'),false));}cleanup.append(card);}
 }
 return {render,buildTime,bytes};
})();
if(typeof module!=='undefined')module.exports=DistributionUI;
