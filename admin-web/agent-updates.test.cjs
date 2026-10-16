const test=require('node:test'),assert=require('node:assert/strict'),ui=require('./agent-updates.js');
test('Agent update bookmarks retain preparation and request identities across re-login',()=>{
 const original={service:'agent-a',release:'ci-1-1',preparation:'prepare-1',request:'update-1'};
 assert.deepEqual(ui.state(ui.bookmark(original)),original);
 assert.deepEqual(ui.state('#agent-updates'),{service:'',release:'',preparation:'',request:''});
});
