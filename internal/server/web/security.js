export function initAccountSecurity({closeMenus}) {
  const element = (id) => document.getElementById(id);
  const dialog = element('accountSecurity');
  const message = element('securityMessage');
  const sections = ['securityStatus', 'securitySetup', 'securityRecovery'];
  let generation = 0;
  let setupId = '';
  let recoveryCodes = [];
  function stage(id) {
    for (const name of sections) element(name).hidden = name !== id;
  }
  async function request(path, body) {
    const response = await fetch(path, {method:body ? 'POST' : 'GET', credentials:'same-origin',
      headers:body ? {'Content-Type':'application/json'} : {}, body:body ? JSON.stringify(body) : undefined,
      signal:AbortSignal.timeout(15000)});
    const value = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(value.message || 'Account security is unavailable. Try again.');
      error.reauthenticate = value.code === 'reauth_required' || value.code === 'signin_required';
      throw error;
    }
    return value;
  }
  function clearSecrets() {
    setupId = '';
    recoveryCodes = [];
    element('securityQR').removeAttribute('src');
    element('securitySecret').textContent = '';
    element('securityCodes').replaceChildren();
    element('securitySetupCode').value = '';
    element('securityProof').value = '';
  }
  async function action(work, loading = '') {
    const current = generation;
    message.textContent = loading;
    element('securityRetry').hidden = true;
    element('securitySignIn').hidden = true;
    for (const button of dialog.querySelectorAll('button[data-security-action]')) button.disabled = true;
    dialog.setAttribute('aria-busy', 'true');
    try { await work(() => dialog.open && generation === current); }
    catch (error) {
      if (dialog.open && generation === current) {
        message.textContent = error.message || 'Could not complete this request. Try again.';
        element('securitySignIn').hidden = !error.reauthenticate;
        element('securityRetry').hidden = !sections.every(id => element(id).hidden) || error.reauthenticate;
      }
    } finally {
      if (dialog.open && generation === current) {
        dialog.removeAttribute('aria-busy');
        for (const button of dialog.querySelectorAll('button[data-security-action]')) button.disabled = false;
      }
    }
  }
  async function loadStatus(active) {
    const state = await request('/api/totp');
    if (!active()) return;
    clearSecrets();
    element('securityAccount').textContent = state.email;
    element('securityState').textContent = state.enabled ? 'On' : 'Off';
    element('securityBegin').hidden = state.enabled;
    element('securityEnabled').hidden = !state.enabled;
    element('securityRemaining').textContent = `${state.recoveryCodesRemaining} recovery codes remaining`;
    stage('securityStatus');
  }
  function showRecovery(codes) {
    clearSecrets();
    recoveryCodes = codes;
    for (const code of codes) {
      const item = document.createElement('li');
      item.textContent = code;
      element('securityCodes').appendChild(item);
    }
    stage('securityRecovery');
    message.textContent = 'Two-factor authentication is on.';
  }
  function open() {
    closeMenus();
    generation++;
    clearSecrets();
    stage('');
    message.textContent = 'Loading account security…';
    dialog.showModal();
    action(loadStatus, 'Loading account security…');
  }
  element('btnSecurity').addEventListener('click', open);
  element('menuSecurity').addEventListener('click', open);
  element('securityRetry').addEventListener('click', () => action(loadStatus, 'Loading account security…'));
  element('securityClose').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    generation++;
    clearSecrets();
    message.textContent = '';
    dialog.removeAttribute('aria-busy');
  });
  element('securityBegin').addEventListener('click', () => action(async (active) => {
    const setup = await request('/api/totp/setup', {});
    if (!active()) return;
    setupId = setup.setupId;
    element('securityQR').src = setup.qr;
    element('securitySecret').textContent = setup.secret;
    stage('securitySetup');
    element('securitySetupCode').focus();
  }));
  element('securitySetupForm').addEventListener('submit', (event) => {
    event.preventDefault();
    action(async (active) => {
      const result = await request('/api/totp/enable', {setupId, code:element('securitySetupCode').value});
      if (active()) showRecovery(result.recoveryCodes);
    });
  });
  element('securityCancelSetup').addEventListener('click', () => action(loadStatus));
  element('securityCopyKey').addEventListener('click', () => action(async (active) => {
    await navigator.clipboard.writeText(element('securitySecret').textContent);
    if (active()) message.textContent = 'Setup key copied.';
  }));
  for (const [id,path] of [['securityDisable','/api/totp/disable'],['securityRegenerate','/api/totp/recovery']]) {
    element(id).addEventListener('click', () => {
      if (!element('securityProof').reportValidity()) return;
      action(async (active) => {
        const result = await request(path, {code:element('securityProof').value});
        if (!active()) return;
        element('securityProof').value = '';
        if (result.enabled) showRecovery(result.recoveryCodes);
        else { await loadStatus(active); if (active()) message.textContent = 'Two-factor authentication is off.'; }
      });
    });
  }
  element('securityCopyCodes').addEventListener('click', () => action(async (active) => {
    await navigator.clipboard.writeText(recoveryCodes.join('\n'));
    if (active()) message.textContent = 'Recovery codes copied.';
  }));
  element('securityDownloadCodes').addEventListener('click', () => {
    const file = new Blob(['ShellGate recovery codes\n\n', recoveryCodes.join('\n'), '\n'], {type:'text/plain'});
    const url = URL.createObjectURL(file);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'shellgate-recovery-codes.txt';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  element('securityDone').addEventListener('click', () => action(loadStatus));
}
