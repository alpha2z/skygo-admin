const {test}=require('node:test');const assert=require('node:assert/strict');const{parseCSV,textValue}=require('./app.js');
test('dependency editor removes empty entries without inventing IDs',()=>assert.deepEqual(parseCSV(' alpha, , beta '),['alpha','beta']));
test('display text preserves untrusted input as plain data',()=>{assert.equal(textValue('<img onerror=alert(1)>'),'<img onerror=alert(1)>');assert.equal(textValue(null),'');assert.equal(textValue({status:'pending'}),'{\n  "status": "pending"\n}')});

test('legacy API catalog absence preserves management access during rollback',async()=>{const {extensionCatalog}=require('./app.js');assert.deepEqual(await extensionCatalog(async()=>{throw Object.assign(Error('not found'),{status:404})}),[]);const pages=[{id:'sample'}];assert.equal(await extensionCatalog(async()=>pages),pages)});
test('catalog authorization and outage errors remain fail closed',async()=>{const {extensionCatalog}=require('./app.js');for(const status of [401,403,500,503,undefined]){const error=Object.assign(Error('unavailable'),{status});await assert.rejects(extensionCatalog(async()=>{throw error}),e=>e===error)}});

test('inventory separates host connectivity from service coverage',()=>{
 const{hostSummary,serviceSummary}=require('./app.js'),now=Date.parse('2026-01-01T00:00:30Z');
 const h={id:'host',active:true,last_seen:'2026-01-01T00:00:29Z',observations:'[]'};
 assert.equal(hostSummary(h,now).status,'online');assert.equal(hostSummary(h,now).service_count,0);
 const s={id:'worker',definition:JSON.stringify({host_id:'host',image:'example/worker:stable'})};
 assert.equal(serviceSummary(s,[h],now).status,'not_reported');assert.equal(serviceSummary(s,[h],now).healthy,'unknown');
 h.observations='not valid';assert.equal(hostSummary(h,now).service_count,null);
});
test('old observations never imply running health or open admission',()=>{
 const{serviceSummary}=require('./app.js'),now=Date.parse('2026-01-01T00:10:00Z');
 const h={id:'host',active:true,last_seen:'2026-01-01T00:00:00Z',observations:[{service:'worker',running:true,healthy:true,extensions:{'game-maintenance':{maintenance:{at:'2026-01-01T00:00:00Z',admission_closed:false}}}}]};
 const s={id:'worker',definition:{host_id:'host'}};let r=serviceSummary(s,[h],now);
 assert.equal(r.status,'host_offline');assert.equal(r.healthy,'unknown');assert.equal(r.maintenance,'unknown');
 h.last_seen='2026-01-01T00:09:59Z';r=serviceSummary(s,[h],now);assert.equal(r.status,'running');assert.equal(r.maintenance,'unknown');
});
