const{test}=require('node:test');const assert=require('node:assert/strict');const ui=require('./publications.js');
test('bookmark restores intent without credentials and retains an unavailable host',()=>{const s={host:'missing-host',release:'ci-1-1',selected:['api'],request:'pub-a'};assert.deepEqual(ui.state(ui.locationFor(s)),s);assert.equal(ui.state('').selected,null);assert.deepEqual(ui.state(ui.locationFor({...s,selected:[]})).selected,[])});
test('default selects only matching changed images; explicit deselection survives refresh',()=>{const t=[{service:'api',available:true,changed:true},{service:'web',available:false,changed:true},{service:'old',available:true,changed:false}];assert.deepEqual(ui.selection(t,null),['api']);assert.deepEqual(ui.selection(t,[]),[]);assert.deepEqual(ui.selection(t,['web']),[])});
test('submission requires every selected fresh receipt but not unselected companion images',()=>{const t=[{service:'api',available:true,changed:true,preparation:'ready'},{service:'web',available:false,changed:true}];assert.equal(ui.canSubmit(t,['api']),true);assert.equal(ui.canSubmit(t,['api','web']),false);assert.equal(ui.canSubmit(t,[]),false);t[0].preparation='working';assert.equal(ui.canSubmit(t,['api']),false)});
test('all HTML script and style assets exist; initial credential submissions disabled',()=>{const fs=require('node:fs'),path=require('node:path'),html=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');for(const match of html.matchAll(/(?:src|href)="\/(.*?\.(?:js|css))"/g))assert.ok(fs.statSync(path.join(__dirname,match[1])).isFile());assert.match(html,/<button disabled>登录/);assert.match(html,/<button disabled>初始化/);assert.match(html,/publications.js/)});
test('system update bookmarks preserve the same durable publication identity',()=>{
 const s={host:'control',release:'ci-1-1',selected:['api'],request:'pub-reconnect'};
 assert.deepEqual(ui.state(ui.locationFor(s,true)),s);
 assert.deepEqual(ui.state(ui.locationFor(ui.state(ui.locationFor(s,true)))),s);
});
test('status watcher reconnects using reads only and stops on terminal outcome',async()=>{
 let next,stopped=0;const paths=[],records=[],errors=[];
 const replies=[Error('offline'),{status:'dispatched'},{status:'rolled_back'}];
 ui.watch({id:'pub-original',api:async(...args)=>{paths.push(args);const r=replies.shift();if(r instanceof Error)throw r;return r;},active:()=>true,onRecord:p=>records.push(p.status),onError:e=>errors.push(e.message),schedule:f=>{next=f;return 1;},cancel:()=>stopped++});
 await next();await next();await next();
 assert.deepEqual(paths,Array(3).fill(['publications/pub-original']));
 assert.deepEqual(records,['dispatched','rolled_back']);assert.deepEqual(errors,['offline']);assert.equal(stopped,1);
});
test('status watcher is bounded and cannot update a departed page',async()=>{
 let next,active=true,calls=0;const errors=[];
 const stop=ui.watch({id:'pub-a',api:async()=>{calls++;return {status:'queued'};},active:()=>active,onRecord:()=>{},onError:e=>errors.push(e.message),limit:2,schedule:f=>{next=f;return 1;},cancel:()=>{}});
 await next();await next();assert.equal(calls,2);assert.match(errors[0],/paused/);
 active=false;await next();assert.equal(calls,2);stop();
});
test('expired session stops polling without resubmitting or clearing identity',async()=>{
 let next,schedules=0,cancelled=false;
 ui.watch({id:'pub-a',api:async()=>{throw Object.assign(Error('expired'),{status:401});},active:()=>true,onRecord:()=>assert.fail(),onError:()=>{},schedule:f=>{next=f;schedules++;},cancel:()=>{cancelled=true;}});
 await next();assert.equal(schedules,1);assert.equal(cancelled,true);
});
test('system update renders inventory before release selection and checks versions using GET only',async()=>{
 class Element{constructor(tag,text=''){this.tag=tag;this.textContent=text;this.children=[];}append(...items){this.children.push(...items);}replaceChildren(...items){this.children=items;}}
 const previous={document:global.document,Option:global.Option,location:global.location,history:global.history,DistributionUI:global.DistributionUI};
 global.document={createElement:tag=>new Element(tag),createTextNode:text=>new Element('text',text)};
 global.Option=class extends Element{constructor(text,value){super('option',text);this.value=value;}};
 global.location={hash:'#system-update?host=control'};global.history={replaceState:(_,__,hash)=>{global.location.hash=hash;}};global.DistributionUI={buildTime:()=> 'Build time unknown'};
 try{
  const root=new Element('root'),calls=[];
  const api=async(...args)=>{calls.push(args);return args[0]==='publication-candidates'?{host_count:1,hosts:[{host_id:'control',targets:[{service:'api',kind:'admin-api',current_image_id:'sha256:observed',provenance:{status:'unknown'},available:false,changed:false,preparation:'unavailable'}]}]}:[];};
  await ui.render({root,api,allowed:()=>true,notice:()=>{},isCurrent:()=>true,refresh:async()=>{},sessionID:1,systemUpdate:true});
  function flatten(e){return [e,...e.children.flatMap(flatten)];}const nodes=flatten(root);
  assert.ok(nodes.some(n=>n.textContent==='Current image: sha256:observed'));
  assert.ok(nodes.some(n=>n.textContent.includes('Source unknown')));
  assert.ok(!nodes.some(n=>n.textContent==='Sync selected images'));
  await nodes.find(n=>n.textContent==='Check versions and status').onclick();
  assert.ok(calls.every(args=>args.length===1));assert.ok(global.location.hash.startsWith('#system-update?'));
 }finally{ui.dispose();for(const[key,value]of Object.entries(previous)){if(value===undefined)delete global[key];else global[key]=value;}}
});
