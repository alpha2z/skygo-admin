/* Optional browser regression: NODE_PATH=<temporary playwright install>/node_modules node scripts/test-web-locales.cjs.
 * Only fixture responses are used; no database, administrator or Agent is contacted.
 */
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const os=require('node:os');
const vm=require('node:vm');
const assets=path.join(__dirname,'../admin-web');
const localeSource=fs.readFileSync(path.join(assets,'locale.js'),'utf8');
const artifactDir=process.env.SKYGO_LOCALE_ARTIFACTS||fs.mkdtempSync(path.join(os.tmpdir(),'skygo-admin-locales-'));
fs.mkdirSync(artifactDir,{recursive:true});
function translator(lang){const ctx={module:{exports:{}},localStorage:{getItem:()=>lang}};vm.runInNewContext(localeSource,ctx);return ctx.module.exports.text;}
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.SKYGO_TEST_CHROMIUM||undefined});
 try{
  for(const lang of ['en','zh-CN','ja']){
   const t=translator(lang),context=await browser.newContext({viewport:{width:1280,height:900}}),page=await context.newPage();
   const errors=[],mutations=[];page.on('pageerror',error=>errors.push(error.message));
   let signedIn=false,delayHosts=null,confirmMutation=false;
   await page.route('http://skygo-admin.test/**',async route=>{
    const request=route.request(),url=new URL(request.url()),resource=url.pathname;
    if(!resource.startsWith('/api/v1/')){
     const file=path.join(assets,resource==='/'?'index.html':resource.slice(1));
     if(!fs.existsSync(file))return route.fulfill({status:404,body:''});
     return route.fulfill({path:file,contentType:file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':'text/html'});
    }
    const endpoint=resource.slice(8);let result=[],status=200;
    if(request.method()!=='GET')mutations.push({endpoint,body:request.postData()});
    if(endpoint==='auth/settings')result={totp_enabled:false};
    else if(endpoint==='bootstrap/status')result={needs_bootstrap:true};
    else if(endpoint==='auth/captcha')result={captcha_id:'fixture',image:'data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg" width="216" height="64"><text x="10" y="40">123456</text></svg>'};
    else if(endpoint==='session'){status=signedIn?200:401;result=signedIn?{id:1,independent_approval_enabled:false,permissions:['ops.read','ops.write','ops.approve','host.manage','build.read','build.write','config.read','config.write','audit.read','admin.manage']}:{error:'invalid credentials'};}
    else if(endpoint==='login'){signedIn=true;result={};}
    else if(endpoint==='hosts'&&request.method()==='POST'){
     if(confirmMutation&&!request.headers()['x-confirmation-code']){status=428;result={confirmation_id:'fixture-confirmation'};}else result={id:'fixture-host',token:'fixture-only'};
    }
    else if(endpoint==='hosts'){
     if(delayHosts)await delayHosts;
     result=[{id:'host-one',active:true,last_seen:new Date().toISOString(),observations:[{service:'Sign in',running:true,healthy:true,plugin_revision:'v1'}]}];
    }
    else if(endpoint==='services')result=[{id:'Sign in',definition:{host_id:'host-one',image:'example/service@sha256:fixture'}}];
    else if(endpoint==='tasks')result=[{service_id:'Sign in',action:'restart',status:'succeeded',requested_by:1,single_confirmation:true,approved_by:1,created_at:'2026-10-08',result:'{"status":"pending"}'}];
    else if(endpoint==='configs')result=[{id:'fixture-config',service_id:'Sign in',content:'{"Language":"日本語"}',sha256:'fixture',created_at:'2026-10-08',requested_by:1,active:true,kind:'json'}];
    else if(endpoint==='admins')result=[{ID:1,Username:'Sign in',Email:'fixture@example.invalid',Role:'superadmin',Active:true}];
    else if(endpoint==='audit')result=[{AuditID:1,OperatorID:1,Action:'Sign in',Target:'fixture',DetailJSON:'{"message":"Sign in"}',PreviousHash:'fixture',EntryHash:'fixture',CreatedAtMS:1}];
    else if(endpoint==='builds')result={enabled:true,config:{repository:'example/admin',workflow:'build.yml'},build_trust:{key_count:1},runs:[{id:1,run_number:1,run_attempt:1,status:'completed',conclusion:'success',head_branch:'main',head_sha:'fixture',commit_message:'Sign in',registration:{status:'registered',release_id:'v1',images:[]}}]};
    else if(endpoint==='release-settings')result={github_configured:false,key_count:1,keys:[],can_add_key:true,github:{auto_register:true,publish_images:true}};
    else if(endpoint==='releases')result=[];
    else if(endpoint==='image-cleanup/resources')result={keep_versions:3,keep_days:7,resources:[]};
    else if(endpoint==='publication-candidates')result={host_count:1,hosts:[{host_id:'host-one',targets:[{service:'api-long-service-name',kind:'admin-api',current_image_id:'sha256:0123456789abcdef',available:true,changed:true,preparation:'ready',image:'example/admin@sha256:abcdef0123456789',provenance:{status:'matched',release_id:'v1',ref:'main',commit:'0123456789abcdef',build_started_at:'2026-10-08T00:00:00Z'}}]}]};
    return route.fulfill({status,contentType:'application/json',body:JSON.stringify(result)});
   });
   await page.goto('http://skygo-admin.test/');
   await page.waitForFunction(()=>!document.getElementById('language').disabled);
   assert.equal(await page.locator('html').getAttribute('lang'),'en');
   if(lang!=='en'){await page.selectOption('#language',lang);await page.waitForFunction(expected=>document.documentElement.lang===expected,lang);}
   await page.waitForFunction(()=>!document.getElementById('language').disabled);
   assert.equal(await page.locator('#auth h2').innerText(),t('Sign in'));
   assert.ok(await page.locator('#language').isVisible());
   await page.screenshot({path:path.join(artifactDir,lang+'-login.png')});
   // Dirty form cancellation must not lose input or change locale.
   await page.locator('#login [name=username]').fill('fixture-admin');
   page.once('dialog',dialog=>dialog.dismiss());
   await page.selectOption('#language',lang==='ja'?'en':'ja');
   assert.equal(await page.locator('#language').inputValue(),lang);
   assert.equal(await page.locator('#login [name=username]').inputValue(),'fixture-admin');
   await page.locator('#login [name=password]').fill('fixture-password');
   await page.locator('#login [name=captcha_code]').fill('123456');
   await page.locator('#login button:not([type="button"])').click();
   await page.locator('#workspace').waitFor({state:'visible'});
   for(const key of ['services','tasks','configs','audit','admins','builds','release-settings','distribution','system-update','publications','hosts']){
    await page.locator(`[data-page="${key}"]`).click();
    await page.waitForFunction(()=>!document.getElementById('language').disabled);
    assert.ok(!(await page.locator('#data').innerText()).includes(t('Loading failed. Refresh to retry.')),key);
    if(['services','admins','audit','builds'].includes(key))assert.ok((await page.locator('#data').innerText()).includes('Sign in'),'raw values must remain unchanged');
    if(key==='system-update'){await page.locator('#data select').last().selectOption('host-one');await page.waitForFunction(()=>!document.getElementById('language').disabled);await page.screenshot({path:path.join(artifactDir,lang+'-update.png')});}
    if(key==='services')await page.screenshot({path:path.join(artifactDir,lang+'-services.png')});
    if(key==='release-settings'){
     const before=page.url();await page.reload();await page.waitForFunction(()=>!document.getElementById('language').disabled);
     assert.equal(page.url(),before);assert.equal(await page.locator('#page-title').innerText(),t('Release settings'));
    }
   }
   // Request lifetime and confirmation dialog both lock language selection.
   let releaseHosts;delayHosts=new Promise(resolve=>releaseHosts=resolve);
   await page.locator('#refresh').click();await page.waitForFunction(()=>document.getElementById('language').disabled);
   releaseHosts();delayHosts=null;await page.waitForFunction(()=>!document.getElementById('language').disabled);
   await page.locator('#forms summary').click();await page.locator('#forms input').fill('new-fixture');confirmMutation=true;
   await page.locator('#forms button').click();await page.locator('#confirmation-dialog').waitFor({state:'visible'});
   assert.equal(await page.locator('#language').isDisabled(),true);
   await page.screenshot({path:path.join(artifactDir,lang+'-confirmation.png')});
   await page.locator('#confirmation-cancel').click();await page.waitForFunction(()=>!document.getElementById('language').disabled);
   const count=mutations.length;page.once('dialog',dialog=>dialog.accept());
   await page.selectOption('#language',lang==='ja'?'en':'ja');
   await page.waitForFunction(expected=>document.documentElement.lang===expected,lang==='ja'?'en':'ja');
   await page.waitForFunction(()=>!document.getElementById('language').disabled);
   assert.equal(new URL(page.url()).hash,'#hosts');assert.equal(mutations.length,count,'switching must not replay a mutation');
   // A narrow viewport must keep the picker and confirmation reachable.
   await page.setViewportSize({width:390,height:844});assert.ok(await page.locator('#language').isVisible());
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
   assert.deepEqual(errors,[]);await context.close();console.log(lang+': login, all navigation, data preservation, dirty forms, request/dialog locks, route restore and no replay passed');
  }
  console.log('Screenshots: '+artifactDir);
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
