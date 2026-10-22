import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import {initAccountSecurity} from './security.js';
const html=readFileSync(new URL('./workspace.html',import.meta.url),'utf8');
const settle=()=>new Promise(setImmediate);
function page(fetcher) {
 const elements=new Map();
 for(const m of html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*\bid="([^"]+)"[^>]*)>/g)){
  elements.set(m[3],{hidden:/\bhidden\b/.test(m[2]),action:/\bdata-security-action\b/.test(m[2]),events:{},value:'',textContent:'',children:[],attrs:{},open:false,
   addEventListener(name,fn){this.events[name]=fn;},setAttribute(name,v){this.attrs[name]=v;},removeAttribute(name){delete this.attrs[name];if(name==='src')delete this.src;},
   replaceChildren(){this.children=[];},appendChild(v){this.children.push(v);},focus(){},reportValidity(){return !!this.value;},
   showModal(){this.open=true;},close(){this.open=false;this.events.close();},querySelectorAll(){return [...elements.values()].filter(e=>e.action);}});
 }
 globalThis.document={getElementById:id=>elements.get(id),createElement:()=>({})};
 globalThis.fetch=fetcher;
 initAccountSecurity({closeMenus(){}});
 return elements;
}
const response=value=>({ok:true,json:async()=>value});
test('security requires explicit open and clears seeds and recovery codes on close',async()=>{
 const calls=[];
 const elements=page(async(path,options)=>{
  calls.push({path,body:options.body&&JSON.parse(options.body)});
  if(path==='/api/totp')return response({email:'user@example.com',enabled:false,recoveryCodesRemaining:0});
  if(path==='/api/totp/setup')return response({setupId:'setup',secret:'TEST-SEED',qr:'data:image/png;base64,test'});
  return response({enabled:true,recoveryCodes:['ONE-TIME-RECOVERY']});
 });
 assert.equal(calls.length,0);
 elements.get('btnSecurity').events.click();await settle();
 assert.equal(elements.get('accountSecurity').open,true);
 assert.equal(elements.get('securityStatus').hidden,false);
 elements.get('securityBegin').events.click();await settle();
 assert.equal(elements.get('securitySecret').textContent,'TEST-SEED');
 assert.equal(elements.get('securitySetup').hidden,false);
 assert.equal(calls.some(c=>c.path==='/api/totp/enable'),false);
 elements.get('securitySetupCode').value='123456';
 elements.get('securitySetupForm').events.submit({preventDefault(){}});await settle();
 assert.deepEqual(calls.at(-1),{path:'/api/totp/enable',body:{setupId:'setup',code:'123456'}});
 assert.equal(elements.get('securitySecret').textContent,'');
 assert.equal(elements.get('securityRecovery').hidden,false);
 assert.equal(elements.get('securityCodes').children[0].textContent,'ONE-TIME-RECOVERY');
 elements.get('securityClose').events.click();
 assert.equal(elements.get('securityCodes').children.length,0);
 assert.equal(elements.get('securityQR').src,undefined);
});
test('late setup response cannot repopulate a closed dialog',async()=>{
 let complete;
 const elements=page(async(path)=>path==='/api/totp'?response({enabled:false,email:'user@example.com',recoveryCodesRemaining:0}):new Promise(resolve=>{complete=resolve;}));
 elements.get('menuSecurity').events.click();await settle();
 elements.get('securityBegin').events.click();await settle();
 elements.get('securityClose').events.click();
 complete(response({setupId:'late',secret:'example',qr:'late'}));await settle();
 assert.equal(elements.get('securitySecret').textContent,'');
 assert.equal(elements.get('accountSecurity').open,false);
});
test('status failure offers retry and recent-auth error offers sign in',async()=>{
 let attempt=0;
 const elements=page(async()=>++attempt===1?{ok:false,json:async()=>({message:'Temporarily unavailable'})}:response({enabled:false,email:'user@example.com',recoveryCodesRemaining:0}));
 elements.get('btnSecurity').events.click();await settle();
 assert.equal(elements.get('securityRetry').hidden,false);
 elements.get('securityRetry').events.click();await settle();
 assert.equal(elements.get('securityStatus').hidden,false);
 globalThis.fetch=async()=>({ok:false,json:async()=>({code:'reauth_required',message:'Sign in again'})});
 elements.get('securityBegin').events.click();await settle();
 assert.equal(elements.get('securitySignIn').hidden,false);
});

test('invalidated session offers sign in rather than a retry loop',async()=>{
 const elements=page(async()=>({ok:false,json:async()=>({code:'signin_required',message:'Sign-in required'})}));
 elements.get('btnSecurity').events.click();await settle();
 assert.equal(elements.get('securitySignIn').hidden,false);
 assert.equal(elements.get('securityRetry').hidden,true);
});
