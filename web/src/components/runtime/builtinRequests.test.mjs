import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import ts from 'typescript';
const output = ts.transpileModule(fs.readFileSync(new URL('./builtinRequests.ts', import.meta.url), 'utf8'), {compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText;
const module={exports:{}};new Function('exports','module',output)(module.exports,module);
const {BuiltinRequests}=module.exports;
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}};
const tick=()=>new Promise(resolve=>setImmediate(resolve));

test('single flight, delayed GET cannot overwrite POST, no GET during mutation',async()=>{
 const reads=[],writes=[],published=[],busy=[];
 const client=new BuiltinRequests(()=>{const d=deferred();reads.push(d);return d.promise},()=>{const d=deferred();writes.push(d);return d.promise},v=>published.push(v),e=>{throw e},v=>busy.push(v),10);
 try {
  void client.refresh();void client.refresh();assert.equal(reads.length,1);
  void client.update(false);void client.refresh();void client.update(true);assert.equal(reads.length,1);assert.equal(writes.length,1);
  writes[0].resolve('disabled');await tick();assert.equal(reads.length,2);
  reads[1].resolve('newest');await tick();reads[0].resolve('stale');await tick();
  assert.deepEqual(published,['disabled','newest']);assert.deepEqual(busy,[true,false]);
 }finally{client.close()}
});
test('slow node does not accumulate polls; hidden and closed views discard requests',async()=>{
 const reads=[],published=[];
 const client=new BuiltinRequests(()=>{const d=deferred();reads.push(d);return d.promise},async()=>0,v=>published.push(v),()=>{},()=>{},5);
 void client.refresh();await new Promise(r=>setTimeout(r,30));assert.equal(reads.length,1);
 client.setVisible(false);reads[0].resolve('hidden');await tick();assert.deepEqual(published,[]);
 client.setVisible(true);assert.equal(reads.length,2);client.close();reads[1].resolve('closed');await tick();assert.deepEqual(published,[]);
});
test('failed node can recover on the next read',async()=>{
 let calls=0;const values=[],errors=[];
 const client=new BuiltinRequests(async()=>{if(++calls===1)throw Error('offline');return 'recovered'},async()=>0,v=>values.push(v),e=>errors.push(e.message),()=>{},1000);
 try{await client.refresh();await client.refresh();assert.deepEqual(errors,['offline']);assert.deepEqual(values,['recovered'])}finally{client.close()}
});
