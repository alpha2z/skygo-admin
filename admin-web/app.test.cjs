const {test}=require('node:test');const assert=require('node:assert/strict');const{parseCSV,textValue}=require('./app.js');
test('dependency editor removes empty entries without inventing IDs',()=>assert.deepEqual(parseCSV(' alpha, , beta '),['alpha','beta']));
test('display text preserves untrusted input as plain data',()=>{assert.equal(textValue('<img onerror=alert(1)>'),'<img onerror=alert(1)>');assert.equal(textValue(null),'');assert.equal(textValue({status:'pending'}),'{\n  "status": "pending"\n}')});
