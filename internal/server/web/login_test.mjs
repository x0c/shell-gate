import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const html=readFileSync(new URL('./login.html',import.meta.url),'utf8');
const source=html.match(/<script type="module">([\s\S]*?)<\/script>/)[1];
const settle=()=>new Promise(setImmediate);

// Exercise the actual page script with controllable network and lifecycle events.
// This checks state transitions; browser layout and screenshots remain separate.
function page(fetch,search='',pending=false,signInWithPasskey=async()=>({})) {
 const elements=new Map();
 for (const match of html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*\bid="([^"]+)"[^>]*)>/g)) {
  const [,tag,attrs,id]=match;
  elements.set(id,{hidden:/\bhidden\b/.test(attrs),disabled:/\bdisabled\b/.test(attrs),textContent:'',events:{},attrs:{},
   focus(){},classList:{add(){}},addEventListener(name,fn){this.events[name]=fn;},setAttribute(name,value){this.attrs[name]=value;},removeAttribute(name){delete this.attrs[name];},
   tag});
 }
 const lifecycle={};
 const navigation=[];
 const history=[];
 const sandbox={document:{getElementById:id=>elements.get(id),querySelector:()=>({classList:{add(){}}}),querySelectorAll:()=>[...elements.values()].filter(e=>e.tag==='button')},
  window:{isSecureContext:true,PublicKeyCredential:class {},addEventListener:(name,fn)=>{lifecycle[name]=fn;}},
  location:{search,pathname:'/login',assign:url=>navigation.push(url),replace:url=>navigation.push(url)},history:{replaceState:(_s,_t,url)=>history.push(url)},
  loadExternalAuth:async()=>({createClient:()=>({auth:{signInWithPasskey}})}),
  fetch:(path,options)=>path==='/api/totp/pending' ? Promise.resolve({ok:true,json:async()=>({required:pending})}) : fetch(path,options),URL,URLSearchParams,AbortSignal,Uint8Array,console};
 vm.runInNewContext(source.replace('await import("/__shellgate/external-auth.js")','await loadExternalAuth()'),sandbox);
 return {elements,lifecycle,navigation,history};
}

test('SSO loads without flashing passkeys, redirects once and restores on back',async()=>{
 let complete;
 const p=page(()=>new Promise(resolve=>{complete=resolve;}));
 assert.equal(p.elements.get('btn').hidden,true);
 assert.equal(p.elements.get('loading').hidden,false);
 complete({ok:true,json:async()=>({authMode:'sso',ssoIssuer:'https://sso.example.com/realm'})});
 await settle();
 const button=p.elements.get('btn');
 assert.equal(p.elements.get('loading').hidden,true);
 assert.equal(button.hidden,false);
 assert.equal(button.disabled,false);
 assert.equal(button.className,'primary');
 assert.equal(button.textContent,'Sign in with SSO');
 assert.equal(p.elements.get('provider').textContent,'sso.example.com');
 assert.equal(p.elements.get('passwordLogin').hidden,true);
 assert.equal(p.elements.get('enroll').hidden,true);
 button.events.click();
 assert.deepEqual(p.navigation,['/api/sso/login']);
 assert.equal(button.disabled,true);
 assert.equal(button.textContent,'Connecting…');
 assert.equal(button.attrs['aria-busy'],'true');
 p.lifecycle.pageshow();
 assert.equal(button.disabled,false);
 assert.equal(button.textContent,'Sign in with SSO');
 assert.equal(button.attrs['aria-busy'],undefined);
});

test('configuration failure stops loading and retry recovers',async()=>{
 let requests=0;
 const p=page(async()=>{
  if (++requests===1) throw new Error('network');
  return {ok:true,json:async()=>({authMode:'sso'})};
 });
 await settle();
 assert.equal(p.elements.get('loading').hidden,true);
 assert.equal(p.elements.get('retry').hidden,false);
 assert.equal(p.elements.get('btn').hidden,true);
 assert.match(p.elements.get('msg').textContent,/retry/);
 p.elements.get('retry').events.click();
 await settle();
 assert.equal(requests,2);
 assert.equal(p.elements.get('retry').hidden,true);
 assert.equal(p.elements.get('btn').disabled,false);
 assert.equal(p.elements.get('msg').textContent,'');
});

test('callback error uses safe actionable copy and removes URL parameters',async()=>{
 const p=page(async()=>({ok:true,json:async()=>({authMode:'sso'})}),'?error=unverified_email&code=sensitive');
 await settle();
 assert.match(p.elements.get('msg').textContent,/Contact your administrator/);
 assert.deepEqual(p.history,['/login']);
 assert.equal(p.elements.get('btn').disabled,false);
});


test('local mode retains password sign-in and account setup',async()=>{
 const p=page(async()=>({ok:true,json:async()=>({authMode:'local'})}));
 await settle();
 assert.equal(p.elements.get('passwordLogin').hidden,false);
 assert.equal(p.elements.get('enroll').hidden,false);
 assert.equal(p.elements.get('btn').hidden,false);
 assert.equal(p.elements.get('signinTitle').hidden,true);
 assert.equal(p.elements.get('provider').hidden,true);
 assert.ok(p.elements.get('passwordLogin').events.submit);
 assert.ok(p.elements.get('register').events.submit);
});


test('pending 2FA hides all primary routes and accepts a recovery code',async()=>{
 let submitted;
 const p=page(async(path,options)=>{
  if(path==='/api/config')return {ok:true,json:async()=>({authMode:'local'})};
  submitted={path,body:JSON.parse(options.body)};
  return {ok:true,json:async()=>({status:'ok'})};
 },'?step=totp',true);
 await settle();
 assert.equal(p.elements.get('primaryAuth').hidden,true);
 assert.equal(p.elements.get('totpAuth').hidden,false);
 assert.deepEqual(p.navigation,[]);
 p.elements.get('useRecovery').events.click();
 assert.equal(p.elements.get('totpLabel').textContent,'Recovery code');
 const input=p.elements.get('totpCode');
 input.value='test recovery code';
 await p.elements.get('totpLogin').events.submit({preventDefault(){}});
 assert.deepEqual(submitted,{path:'/api/totp/verify',body:{code:'test recovery code'}});
 assert.equal(input.value,'');
 assert.deepEqual(p.navigation,['/']);
});

test('incorrect second factor stays on verification with an actionable error',async()=>{
 const p=page(async(path)=>({ok:path==='/api/config',json:async()=>path==='/api/config'?{authMode:'sso'}:{message:'Code already used. Try a new code.'}}),'',true);
 await settle();
 p.elements.get('totpCode').value='123456';
 await p.elements.get('totpLogin').events.submit({preventDefault(){}});
 assert.match(p.elements.get('totpError').textContent,/Try a new code/);
 assert.equal(p.elements.get('verifyTOTP').disabled,false);
 assert.deepEqual(p.navigation,[]);
});

for (const mode of ['local','supabase']) {
 test(`${mode} retains passkey entry alongside optional SSO`,async()=>{
  let passkeyCalls=0;let exchange;
  const p=page(async(path,options)=>{
   if(path==='/api/config')return {ok:true,json:async()=>({authMode:mode,ssoEnabled:true,ssoIssuer:'https://login.example.com'})};
   exchange={path,body:JSON.parse(options.body)};
   return {ok:true,json:async()=>({status:'ok'})};
  },'',false,async()=>{passkeyCalls++;return {data:{session:{access_token:'fixture-token'}}}});
  await settle();
  const primary=p.elements.get('btn'),sso=p.elements.get('btnSSO');
  assert.equal(primary.hidden,false);
  assert.equal(sso.hidden,false);
  assert.equal(primary.disabled,false);
  assert.equal(sso.disabled,false);
  assert.notEqual(primary.textContent,'Sign in with SSO');
  if(mode==='supabase'){
   await primary.events.click();
   assert.equal(passkeyCalls,1);
   assert.deepEqual(exchange,{path:'/api/session',body:{access_token:'fixture-token'}});
  }
  sso.events.click();
  assert.equal(sso.disabled,true);
  assert.equal(primary.disabled,false);
  assert.equal(p.navigation.at(-1),'/api/sso/login');
  p.lifecycle.pageshow();
  assert.equal(sso.disabled,false);
 });
}
