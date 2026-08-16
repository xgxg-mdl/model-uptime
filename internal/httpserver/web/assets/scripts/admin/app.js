import {
  createAdminClient,
  createToast,
  focusWithoutScroll,
  setButtonPending,
} from './shared.js';
import { createServicesController } from './services.js';
import { createTelegramController } from './telegram.js';
import { createUpdateController } from './updater.js';
import { createPageSettingsController } from './page-settings.js';

export const TOKEN_KEY = 'admin_token';

export function startAdminApp({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  fetch: fetchImpl = globalThis.fetch,
  storage = globalThis.sessionStorage,
  location: locationRef = globalThis.location,
  confirm: confirmAction = globalThis.confirm,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  now = () => Date.now(),
} = {}) {
  const toast = createToast({ document: documentRef, schedule, cancel });
  const token = () => storage.getItem(TOKEN_KEY) || '';
  const logout = () => {
    storage.removeItem(TOKEN_KEY);
    locationRef.reload();
  };
  const api = createAdminClient({ fetch: fetchImpl, token, onUnauthorized: logout });

  const telegram = createTelegramController({
    document: documentRef,
    window: windowRef,
    api,
    toast,
    confirm: confirmAction,
  });
  const services = createServicesController({
    document: documentRef,
    window: windowRef,
    api,
    toast,
    confirm: confirmAction,
    onServicesChanged: nextServices => telegram.setServices(nextServices),
    onServiceDeleted: () => telegram.load(),
  });
  const pageSettings = createPageSettingsController({ document: documentRef, api, toast });
  const updater = createUpdateController({
    document: documentRef,
    api,
    toast,
    storage,
    confirm: confirmAction,
    schedule,
    cancel,
    now,
  });

  function enterApp() {
    documentRef.getElementById('auth-loading').hidden = true;
    documentRef.getElementById('login-view').hidden = true;
    documentRef.getElementById('setup-view').hidden = true;
    documentRef.getElementById('app-view').hidden = false;
    documentRef.getElementById('logout-btn').hidden = false;
    void services.load();
    void telegram.load();
    void pageSettings.load();
    void updater.load();
    focusWithoutScroll(documentRef.getElementById('app-view'));
  }

  async function initView() {
    if (token()) {
      enterApp();
      return;
    }
    let configured = true;
    try {
      const response = await fetchImpl('/api/admin/setup-status', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json();
      configured = Boolean(data.token_configured);
    } catch {
      configured = true;
    }
    documentRef.getElementById('auth-loading').hidden = true;
    const viewID = configured ? 'login-view' : 'setup-view';
    documentRef.getElementById(viewID).hidden = false;
    focusWithoutScroll(documentRef.getElementById(configured ? 'login-token' : 'setup-token'));
  }

  documentRef.getElementById('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    const value = documentRef.getElementById('login-token').value.trim();
    const submit = documentRef.getElementById('login-submit');
    if (submit.getAttribute?.('aria-busy') === 'true') return;
    if (!value) { toast('Enter the admin password.'); return; }
    setButtonPending(submit, true, 'checking…');
    try {
      const response = await fetchImpl('/api/admin/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: value }),
      });
      if (!response.ok) {
        toast('Invalid password.');
        return;
      }
      storage.setItem(TOKEN_KEY, value);
      enterApp();
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(submit, false);
    }
  });

  documentRef.getElementById('setup-form').addEventListener('submit', async event => {
    event.preventDefault();
    const value = documentRef.getElementById('setup-token').value.trim();
    const confirmation = documentRef.getElementById('setup-confirm').value.trim();
    const submit = documentRef.getElementById('setup-submit');
    if (submit.getAttribute?.('aria-busy') === 'true') return;
    if (value.length < 8) { toast('Password must contain at least 8 characters.'); return; }
    if (value !== confirmation) { toast('Passwords do not match.'); return; }
    setButtonPending(submit, true, 'saving…');
    try {
      const response = await fetchImpl('/api/admin/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: value }),
      });
      if (!response.ok) {
        const data = await response.json().catch(() => ({}));
        toast(data.error || 'Could not set the password.');
        return;
      }
      storage.setItem(TOKEN_KEY, value);
      enterApp();
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(submit, false);
    }
  });

  documentRef.getElementById('logout-btn').addEventListener('click', logout);
  void initView();

  return {
    initView,
    enterApp,
    logout,
    services,
    telegram,
    pageSettings,
    updater,
  };
}

if (typeof document !== 'undefined') startAdminApp();
