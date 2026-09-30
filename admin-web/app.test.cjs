const {test}=require('node:test');const assert=require('node:assert/strict');const{parseCSV,textValue}=require('./app.js');
test('dependency editor removes empty entries without inventing IDs',()=>assert.deepEqual(parseCSV(' alpha, , beta '),['alpha','beta']));
test('display text preserves untrusted input as plain data',()=>{assert.equal(textValue('<img onerror=alert(1)>'),'<img onerror=alert(1)>');assert.equal(textValue(null),'');assert.equal(textValue({status:'pending'}),'{\n  "status": "pending"\n}')});

test('legacy API catalog absence preserves management access during rollback',async()=>{const {extensionCatalog}=require('./app.js');assert.deepEqual(await extensionCatalog(async()=>{throw Object.assign(Error('not found'),{status:404})}),[]);const pages=[{id:'sample'}];assert.equal(await extensionCatalog(async()=>pages),pages)});
test('catalog authorization and outage errors remain fail closed',async()=>{const {extensionCatalog}=require('./app.js');for(const status of [401,403,500,503,undefined]){const error=Object.assign(Error('unavailable'),{status});await assert.rejects(extensionCatalog(async()=>{throw error}),e=>e===error)}});
