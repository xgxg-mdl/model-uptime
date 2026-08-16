import { createAdminClient, createToast } from './shared.js';
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
    documentRef.getElementById('login-view').hidden = true;
    documentRef.getElementById('setup-view').hidden = true;
    documentRef.getElementById('app-view').hidden = false;
    documentRef.getElementById('logout-btn').hidden = false;
    void services.load();
    void telegram.load();
    void pageSettings.load();
    void updater.load();
  }

  async function initView() {
    if (token()) {
      enterApp();
      return;
    }
    let configured = true;
    try {
      const response = await fetchImpl('/api/admin/setup-status', { cache: 'no-store' });
      const data = await response.json();
      configured = Boolean(data.token_configured);
    } catch {
      configured = true;
    }
    documentRef.getElementById(configured ? 'login-view' : 'setup-view').hidden = false;
  }

  documentRef.getElementById('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    const value = documentRef.getElementById('login-token').value.trim();
    try {
      const response = await fetchImpl('/api/admin/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: value }),
      });
      if (!response.ok) {
        toast('密码无效');
        return;
      }
      storage.setItem(TOKEN_KEY, value);
      enterApp();
    } catch (error) {
      toast(error.message);
    }
  });

  documentRef.getElementById('setup-form').addEventListener('submit', async event => {
    event.preventDefault();
    const value = documentRef.getElementById('setup-token').value.trim();
    const confirmation = documentRef.getElementById('setup-confirm').value.trim();
    if (value.length < 8) { toast('密码至少 8 个字符'); return; }
    if (value !== confirmation) { toast('两次输入的密码不一致'); return; }
    try {
      const response = await fetchImpl('/api/admin/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: value }),
      });
      if (!response.ok) {
        const data = await response.json().catch(() => ({}));
        toast(data.error || '设置失败');
        return;
      }
      storage.setItem(TOKEN_KEY, value);
      enterApp();
    } catch (error) {
      toast(error.message);
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
