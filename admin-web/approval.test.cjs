const {test}=require('node:test');
const assert=require('node:assert/strict');
const ui=require('./publications.js');

test('publication mode changes the action, with one durable submission and no automatic approve call',async()=>{
 class Element{constructor(tag,text=''){this.tag=tag;this.textContent=text;this.children=[];}append(...items){this.children.push(...items);}replaceChildren(...items){this.children=items;}}
 const previous={document:global.document,Option:global.Option,location:global.location,history:global.history,DistributionUI:global.DistributionUI,crypto:global.crypto,AdminLocale:global.AdminLocale};
 global.document={createElement:tag=>new Element(tag)};
 global.Option=class extends Element{constructor(text,value){super('option',text);this.value=value;}};
 global.location={hash:''};global.history={replaceState:()=>{}};global.DistributionUI={buildTime:()=> 'fixture'};
 if(!global.crypto)global.crypto=require('node:crypto').webcrypto;
 try{
  for(const language of ['en','zh-CN','ja'])for(const independent of [false,true]){
   const localeContext={module:{exports:{}},localStorage:{getItem:()=>language}};
   require('node:vm').runInNewContext(require('node:fs').readFileSync(__dirname+'/locale.js','utf8'),localeContext);global.AdminLocale=localeContext.module.exports;
   const root=new Element('root'),calls=[],notices=[];
   const api=async(path,method,body)=>{
    calls.push([path,method,body]);
    if(path==='publications'&&method==='POST')return{id:body.request_id,status:independent?'pending':'queued',single_confirmation:!independent};
    if(path==='publications')return[];
    if(path==='releases')return[{id:'release',manifest:{build:{ref:'main',source_commit:'abcdef1234'}}}];
    if(path.startsWith('publication-candidates'))return{host_count:1,hosts:[{host_id:'host',targets:[{service:'api',kind:'admin-api',current_image_id:'old',image:'new',available:true,changed:true,preparation:'ready',provenance:{status:'unknown'}}]}]};
    throw Error('unexpected request '+path);
   };
   await ui.render({root,api,allowed:p=>p!=='ops.approve',notice:s=>notices.push(s),isCurrent:()=>true,refresh:async()=>{},sessionID:1,independentApprovalEnabled:independent,publicationState:{host:'host',release:'release',selected:['api'],request:''},savePublicationState:()=>{}});
   const flatten=e=>[e,...e.children.flatMap(flatten)];
   const action=flatten(root).find(n=>n.textContent===global.AdminLocale.text(independent?'Submit for independent approval':'Confirm execution'));
   assert.ok(action);assert.equal(action.disabled,false);await action.onclick();
   const mutations=calls.filter(c=>c[1]==='POST');assert.equal(mutations.length,1);assert.equal(mutations[0][0],'publications');assert.ok(mutations[0][2].request_id);
   assert.match(notices[0],independent?/different administrator/:/Execution submitted/);
  }
 }finally{ui.dispose();for(const[key,value]of Object.entries(previous)){if(value===undefined)delete global[key];else global[key]=value;}}
});
