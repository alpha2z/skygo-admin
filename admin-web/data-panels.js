// Generic data presentation; all metric semantics belong to the provider.
export function segments(points) {
 const runs=[]; let run=[];
 for(const p of points){if(p.value===null||!Number.isFinite(p.value)){if(run.length)runs.push(run);run=[];}else run.push(p);}
 if(run.length)runs.push(run);return runs;
}
export function stepFor(seconds){return Math.max(15,Math.ceil(seconds/999));}
export async function render(ctx){
 const {root,api,isCurrent,page}=ctx;
 const t=ctx.t||((key,params={})=>key.replace(/\{(\w+)\}/g,(m,k)=>Object.hasOwn(params,k)?String(params[k]):m));
 const locale=ctx.locale||'en';
 const split=page.id.indexOf('.'),provider=page.id.slice(0,split),panelID=page.id.slice(split+1);
 const base='plugins/'+encodeURIComponent(provider)+'/data';
 const life=new AbortController(); let timer=null,cycle=null,revision=0;
 const stop=()=>{life.abort();cycle?.abort();clearTimeout(timer);};ctx.onDispose(stop);
 const node=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n;};
 const get=(path,signal)=>api(base+path,'GET',undefined,{}, {signal});
 const catalog=await get('/catalog',life.signal);if(!isCurrent()||life.signal.aborted)return;
 const panel=catalog.panels.find(p=>p.id===panelID);if(!panel)throw Error(t('Data panel unavailable'));
 root.replaceChildren();const controls=node('div'),range=node('select');controls.className='data-controls';range.setAttribute('aria-label',t('History range'));
 for(const [title,seconds]of [['1 hour',3600],['24 hours',86400],['7 days',604800],['15 days',1296000]]){const o=node('option',t(title));o.value=seconds;range.append(o);}controls.append(range);
 const filters=[];const dimensions=[...new Set(catalog.datasets.flatMap(d=>d.dimensions||[]))];
 for(const dimension of dimensions){const label=node('label',dimension+' '),input=node('input');input.placeholder=t('All');input.setAttribute('aria-label',t('{dimension} filter',{dimension}));label.append(input);controls.append(label);filters.push([dimension,input]);}
 const group=node('select');group.setAttribute('aria-label',t('Group by'));for(const dimension of ['',...dimensions]){const o=node('option',dimension||t('Total'));o.value=dimension;group.append(o);}controls.append(group);root.append(controls);
 const boxes=panel.widgets.map(widget=>{const section=node('section');section.className='data-widget';section.append(node('h3',widget.title));const body=node('div');section.append(body);root.append(section);return {widget,body,page:1};});
 function chart(body,result,unit){
 const points=(result.series||[]).flatMap(s=>s.points).filter(p=>p.value!==null&&Number.isFinite(p.value));
 if(!points.length){body.append(node('p',t('No samples in this range')));return;}
 const times=points.map(p=>Date.parse(p.at)),minT=Math.min(...times),maxT=Math.max(...times),minV=Math.min(0,...points.map(p=>p.value)),maxV=Math.max(...points.map(p=>p.value),minV+1);
 const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 800 220');svg.setAttribute('role','img');svg.setAttribute('aria-label',t('Historical {unit}',{unit}));svg.style.width='100%';svg.style.maxHeight='280px';
 const colors=['#327c9d','#71977b','#c86558','#9b6baf','#b28940'];
 for(const [i,s]of (result.series||[]).entries()){
 for(const run of segments(s.points)){const line=document.createElementNS(svg.namespaceURI,'polyline');line.setAttribute('fill','none');line.setAttribute('stroke',colors[i%colors.length]);line.setAttribute('stroke-width','2');line.setAttribute('points',run.map(p=>`${40+(Date.parse(p.at)-minT)/Math.max(1,maxT-minT)*740},${190-(p.value-minV)/(maxV-minV)*170}`).join(' '));svg.append(line);}
 const label=node('p',Object.values(s.labels||{}).join(' / ')||t('Total'));label.style.color=colors[i%colors.length];body.append(label);
 }
 body.append(svg,node('p',`${minV.toLocaleString(locale)} – ${maxV.toLocaleString(locale)} ${unit} | ${new Date(minT).toLocaleString(locale)} – ${new Date(maxT).toLocaleString(locale)}`));
 }
 async function update(){
 clearTimeout(timer);cycle?.abort();cycle=new AbortController();const current=++revision,signal=cycle.signal;
 if(!isCurrent()||life.signal.aborted){stop();return;}
 await Promise.all(boxes.map(async box=>{const {widget,body}=box,d=catalog.datasets.find(d=>d.id===widget.dataset);let mode={card:'current',chart:'series',table:'rows'}[widget.kind],q=new URLSearchParams();for(const [name,input]of filters){if(input.value.trim()&&(d.dimensions||[]).includes(name))q.set('filter.'+name,input.value.trim());}if(mode!=='rows'&&group.value&&(d.dimensions||[]).includes(group.value))q.set('group',group.value);
 if(mode==='series'){const end=new Date(),seconds=Number(range.value);q.set('start',new Date(end-seconds*1000).toISOString());q.set('end',end.toISOString());q.set('step',stepFor(seconds));}
 if(mode==='rows'){q.set('page',box.page);q.set('limit',25);if(box.sort){q.set('sort',box.sort);q.set('order',box.order);}}
 try{const result=await get('/datasets/'+encodeURIComponent(d.id)+'/'+mode+'?'+q,signal);if(!isCurrent()||current!==revision||signal.aborted)return;
 body.replaceChildren();body.append(node('p',result.state+' · '+(result.sampled_at?new Date(result.sampled_at).toLocaleString(locale):t('No sample'))));
 for(const warning of result.warnings||[])body.append(node('p',warning));
 if(mode==='current'){for(const v of result.values||[])body.append(node('strong',(Object.values(v.labels||{}).join(' / ')||'')+' '+(v.value===null?t('Unknown'):v.value.toLocaleString(locale))+' '+d.unit));}
 else if(mode==='series')chart(body,result,d.unit);
 else {const table=node('table'),head=node('tr');for(const f of d.fields||[]){const th=node('th'),sort=node('button',f.title);sort.onclick=()=>{box.order=box.sort===f.id&&box.order==='asc'?'desc':'asc';box.sort=f.id;box.page=1;update();};th.append(sort);head.append(th);}table.append(head);for(const row of result.rows||[]){const tr=node('tr');for(const f of d.fields||[])tr.append(node('td',row[f.id]===null||row[f.id]===undefined?t('Unknown'):String(row[f.id])));table.append(tr);}body.append(table);for(const [title,delta,disabled]of [['Previous',-1,box.page<=1],['Next',1,box.page*25>=result.total]]){const b=node('button',t(title));b.disabled=disabled;b.onclick=()=>{box.page+=delta;update();};body.append(b);}}
 }catch(error){if(!isCurrent()||current!==revision||signal.aborted)return;body.replaceChildren(node('p',t('Unavailable: {error}',{error:t(error.message)})));}
 }));
 if(isCurrent()&&!life.signal.aborted&&current===revision)timer=setTimeout(update,15000);
 }
 range.onchange=update;group.onchange=update;for(const [,input]of filters)input.onchange=()=>{for(const box of boxes)box.page=1;update();};await update();
}
