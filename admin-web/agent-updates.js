'use strict';
const AgentUpdateUI = (() => {
 const t = value => typeof AdminLocale === 'undefined' ? value : AdminLocale.text(value);
 function state(hash) { const p=new URLSearchParams(hash.split('?')[1]||'');return Object.fromEntries(['service','release','preparation','request'].map(k=>[k,p.get(k)||''])); }
 function bookmark(s) { return '#agent-updates?'+new URLSearchParams(s).toString(); }
 async function render(ctx) {
  const {root,api,allowed,notice,isCurrent,refresh}=ctx, s=state(location.hash);
  const [services,hosts,releases]=await Promise.all([api('services'),api('hosts'),api('releases')]);if(!isCurrent())return;
  root.replaceChildren();const node=(tag,text)=>{const n=document.createElement(tag);if(text)n.textContent=t(text);return n;};
  const select=(label,key,values)=>{const l=node('label',label),n=node('select');n.append(new Option(t('Select'),''));for(const [id,title] of values)n.append(new Option(title,id));if(s[key]&&!values.some(v=>v[0]===s[key]))n.append(new Option(s[key],s[key]));n.value=s[key];n.onchange=()=>{s[key]=n.value;s.preparation='';s.request='';history.replaceState(null,'',bookmark(s));refresh();};l.append(n);root.append(l);};
  const defs=services.map(x=>{try{return JSON.parse(x.definition);}catch{return {};}}).filter(d=>d.control_kind==='ops-agent');
  select('Service','service',defs.map(d=>[d.id,d.host_id+' / '+d.id]));select('Version','release',releases.filter(r=>r.manifest?.images?.some(i=>i.service==='ops-agent')).map(r=>[r.id,r.id]));
  const def=defs.find(d=>d.id===s.service),host=hosts.find(h=>h.id===def?.host_id);let observations=[];try{observations=JSON.parse(host?.observations||'[]');}catch{}
  const o=observations.find(o=>o.service===s.service),capable=o?.capabilities?.includes('agent.self-update.v1');
  if(!capable)root.append(node('p','Install the transitional Agent and independent updater first.'));
  const button=(label,fn,enabled=true)=>{const b=node('button',label);b.disabled=!enabled;b.onclick=async()=>{b.disabled=true;try{await fn();if(isCurrent())await refresh();}catch(e){notice(e.message);if(isCurrent())b.disabled=false;}};root.append(b);};
  const getTask=async id=>{try{return await api('tasks/'+encodeURIComponent(id));}catch(e){if(e.status===404)return undefined;throw e;}};let prepared,task;
  if(s.preparation){prepared=await getTask(s.preparation);if(!isCurrent())return;root.append(node('p',t('Image preparation')+': '+t(prepared?.status||'pending')));}
  if(s.request){task=await getTask(s.request);if(!isCurrent())return;let result={};try{result=JSON.parse(task?.result||'{}');}catch{}root.append(node('p',t('Agent update')+': '+t(task?.status||'pending')+' · '+t(result.code||'')));}
  const editable=allowed('ops.write')&&allowed('build.read')&&capable&&s.release&&s.service;
  const pending=task&&['pending','queued','dispatched','uncertain'].includes(task.status);
  button('Prepare Agent image',async()=>{if(!s.preparation||prepared?.status==='failed'||prepared?.status==='expired'||prepared?.status==='succeeded'){s.preparation=crypto.randomUUID();s.request='';history.replaceState(null,'',bookmark(s));}await api('agent-updates/prepare','POST',{request_id:s.preparation,service:s.service,release_id:s.release});},editable&&!pending&&(!prepared||['failed','expired','succeeded'].includes(prepared.status)));
  button(ctx.independentApprovalEnabled?'Submit for approval':'Confirm execution',async()=>{if(!s.request){s.request=crypto.randomUUID();history.replaceState(null,'',bookmark(s));}await api('agent-updates','POST',{request_id:s.request,service:s.service,release_id:s.release,preparation_id:s.preparation});},editable&&!pending&&prepared?.status==='succeeded'&&(!task));
  if(task?.status==='pending'&&allowed('ops.approve')&&task.requested_by!==ctx.sessionID){button('Approve',()=>api('tasks/'+encodeURIComponent(s.request)+'/approve','POST'));button('Reject',()=>api('tasks/'+encodeURIComponent(s.request)+'/reject','POST'));}
  root.append(node('p','Refresh to observe progress. Agent updates and API/Web updates run separately.'));
 }
 return {state,bookmark,render};
})();
if(typeof module!=='undefined')module.exports=AgentUpdateUI;
