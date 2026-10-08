const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/locale.js','utf8');
function load(saved,broken=false,code=source){
 const values=new Map(saved===undefined?[]:[['skygo-admin.locale',saved]]);
 const context={module:{exports:{}},localStorage:{getItem:key=>{if(broken)throw Error('blocked');return values.get(key);},setItem:(key,value)=>{if(broken)throw Error('blocked');values.set(key,value);}}};
 vm.runInNewContext(code,context);return {locale:context.module.exports,values};
}
test('first visit is English; only supported persisted preferences are restored',()=>{
 assert.equal(load().locale.locale,'en');assert.equal(load('fr').locale.locale,'en');
 for(const language of ['en','zh-CN','ja'])assert.equal(load(language).locale.locale,language);
 const {locale,values}=load();assert.equal(locale.setLocale('ja'),true);assert.equal(values.get('skygo-admin.locale'),'ja');
 assert.equal(locale.setLocale('fr'),false);assert.equal(locale.locale,'ja');
});
test('unavailable storage keeps the English UI usable without claiming persistence',()=>{
 const {locale}=load('ja',true);assert.equal(locale.locale,'en');assert.equal(locale.text('Sign in'),'Sign in');assert.equal(locale.setLocale('ja'),false);
});
test('all messages have three translations and identical named parameters',()=>{
 const messages=JSON.parse(source.match(/const messages = (\{[\s\S]*?\n \});/)[1]);
 const parameters=s=>[...s.matchAll(/\{(\w+)\}/g)].map(m=>m[1]).sort();
 for(const [key,entry]of Object.entries(messages))for(const lang of ['en','zh-CN','ja']){
  assert.equal(typeof entry[lang],'string',`${lang}: ${key}`);assert.ok(entry[lang].length,`${lang}: ${key}`);
  assert.deepEqual(parameters(entry[lang]),parameters(entry.en),`${lang}: ${key}`);
 }
});
test('missing translation falls back to English; unknown data and hostile parameters remain plain text',()=>{
 const code=source.replace('"ja": "サインイン"','"ja": ""');const {locale}=load('ja',false,code);
 assert.equal(locale.text('Sign in'),'Sign in');assert.equal(locale.text('unknown.data {id}',{id:1}),'unknown.data {id}');
 assert.equal(locale.text('toString'),'toString');assert.equal(locale.text(null),null);
 assert.equal(locale.text('Execution {id}',{id:'<script>{id}</script>'}),'実行タスク <script>{id}</script>');
});
test('all marked static messages are present and HTML language is updated',()=>{
 const html=fs.readFileSync(__dirname+'/index.html','utf8');
 const keys=[...html.matchAll(/data-i18n(?:-(?:alt|aria-label|placeholder|title))?="([^"]+)"/g)].map(m=>m[1].replaceAll('&amp;','&'));
 for(const lang of ['zh-CN','ja']){
  const {locale}=load(lang);for(const key of keys)assert.notEqual(locale.text(key),key,key);
  const node={dataset:{i18n:'Sign in'},textContent:''};const doc={documentElement:{lang:'en'},querySelectorAll:selector=>selector==='[data-i18n]'?[node]:[]};
  locale.apply(doc);assert.equal(doc.documentElement.lang,lang);assert.equal(node.textContent,locale.text('Sign in'));
 }
});
test('dirty form detection includes edits, checkboxes and selects but ignores reset fields',()=>{
 const {hasUnsavedChanges}=require('./app.js');const inputs=[];const root={querySelectorAll:()=>inputs};
 assert.equal(hasUnsavedChanges(root),false);inputs.push({tagName:'INPUT',type:'text',value:'typed',defaultValue:''});assert.equal(hasUnsavedChanges(root),true);
 inputs[0].value='';assert.equal(hasUnsavedChanges(root),false);inputs.push({tagName:'INPUT',type:'checkbox',checked:true,defaultChecked:false});assert.equal(hasUnsavedChanges(root),true);
 inputs.pop();inputs.push({tagName:'SELECT',selectedIndex:1,options:[{defaultSelected:false},{defaultSelected:false}]});assert.equal(hasUnsavedChanges(root),true);
 inputs[1].options[1].defaultSelected=true;assert.equal(hasUnsavedChanges(root),false);
});
