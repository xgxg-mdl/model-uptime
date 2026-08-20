import { escapeHTML, revealPanel } from './shared.js';

export const DEFAULT_TELEGRAM_LANGUAGE = 'zh-CN';

export function normalizeTelegramTemplates(templates = {}) {
  return {
    'zh-CN': templates.zh || templates['zh-CN'] || '',
    'en-US': templates.en || templates['en-US'] || '',
  };
}

export function normalizeTelegramSubscription(subscription, templates = {}) {
  const language = templates[subscription.language] ? subscription.language : DEFAULT_TELEGRAM_LANGUAGE;
  return {
    id: subscription.id || '',
    name: subscription.name || '',
    enabled: subscription.enabled !== false,
    chat_id: String(subscription.chat_id || ''),
    language,
    service_ids: Array.isArray(subscription.service_ids) ? subscription.service_ids.slice() : [],
    template: subscription.template || templates[language],
  };
}

export async function sendTelegramTest({ subscription, results, save, api, kind = 'event' }) {
  const setResult = (state, text) =>
    results.forEach(result => {
      const base = result.id === 'tg-test-result' ? 'test-result feedback-in' : 'subscription-test-result feedback-in';
      result.className = `${base} ${state}`.trim();
      result.textContent = text;
    });
  setResult('', 'saving config…');
  try {
    await save({ quiet: true });
    setResult('', kind === 'daily' ? 'sending daily test…' : 'sending test message…');
    await api('/api/admin/telegram/test', {
      method: 'POST',
      body: JSON.stringify(
        kind === 'daily' ? { subscription_id: subscription.id, kind } : { subscription_id: subscription.id },
      ),
    });
    setResult('ok', 'test message sent');
    return true;
  } catch (error) {
    setResult('bad', error.message);
    return false;
  }
}

export function createTelegramController({
  document: documentRef,
  window: windowRef = globalThis.window,
  windows,
  api,
  toast,
  confirm: confirmAction = globalThis.confirm,
} = {}) {
  let services = [];
  let config = { bot_token: '', subscriptions: [] };
  let templates = normalizeTelegramTemplates();
  let editingIndex = null;
  let tokenConfigured = false;
  const element = id => documentRef.getElementById(id);
  const showWindow = id => {
    if (windows) windows.open(id);
    else revealPanel(element(id), windowRef);
  };
  const hideWindow = id => {
    if (windows) windows.close(id);
    else element(id).classList.add('hidden');
  };

  function selectedServiceIDs() {
    return Array.from(documentRef.querySelectorAll('#tg-model-picker input:checked')).map(input => input.value);
  }

  function selectionForServiceRefresh() {
    const choices = documentRef.querySelectorAll('#tg-model-picker input');
    if (choices.length || editingIndex === null) return selectedServiceIDs();
    return config.subscriptions[editingIndex]?.service_ids || [];
  }

  function renderModelPicker(selectedIDs = []) {
    const picker = element('tg-model-picker');
    const selected = new Set(selectedIDs);
    if (!services.length) {
      picker.innerHTML = '<div class="empty" style="grid-column:1/-1;">no models available</div>';
      return;
    }
    picker.innerHTML = services
      .map(service => {
        const displayModel = service.model || service.name || service.id;
        const detail = service.name && service.name !== displayModel ? ` · ${service.name}` : '';
        const disabled = service.enabled ? '' : ' · disabled';
        return `<label class="check-row ${service.enabled ? '' : 'disabled-label'}">
        <input type="checkbox" value="${escapeHTML(service.id)}" ${selected.has(service.id) ? 'checked' : ''} />
        <span class="model-label"><b>${escapeHTML(displayModel)}</b>${escapeHTML(detail)} <span class="tag">#${escapeHTML(service.id)}</span>${disabled}</span>
      </label>`;
      })
      .join('');
  }

  function setServices(nextServices) {
    const selected = selectionForServiceRefresh();
    services = nextServices.slice();
    renderModelPicker(selected);
  }

  function renderSubscriptions() {
    const list = element('tg-subscription-list');
    if (!config.subscriptions.length) {
      list.innerHTML = '<div class="empty">no subscriptions yet</div>';
      return;
    }
    list.innerHTML = config.subscriptions
      .map((subscription, index) => {
        const serviceCount = subscription.service_ids ? subscription.service_ids.length : 0;
        return `<div class="subscription-row">
        <div class="subscription-meta">
          <b><span class="dot ${subscription.enabled ? 'on' : 'off'}"></span>${escapeHTML(subscription.name || subscription.id)}</b>
          <div class="subscription-summary">#${escapeHTML(subscription.id)} · chat ${escapeHTML(subscription.chat_id)} · ${escapeHTML(subscription.language)} · ${serviceCount} model${serviceCount === 1 ? '' : 's'}</div>
          <div class="subscription-test-result feedback-in hidden" data-tg-test-status="${index}" role="status" aria-live="polite" aria-atomic="true"></div>
        </div>
        <div class="actions">
          <button class="btn" type="button" data-tg-action="edit" data-index="${index}">edit</button>
          <button class="btn" type="button" data-tg-action="test" data-index="${index}">test</button>
          <button class="btn bad" type="button" data-tg-action="delete" data-index="${index}">del</button>
        </div>
      </div>`;
      })
      .join('');
    list.querySelectorAll('[data-tg-action]').forEach(button => {
      button.addEventListener('click', () => {
        const index = Number(button.dataset.index);
        if (button.dataset.tgAction === 'edit') openEditor(index);
        else if (button.dataset.tgAction === 'test') void testSubscription(index);
        else if (button.dataset.tgAction === 'delete') deleteSubscription(index);
      });
    });
  }

  async function load() {
    try {
      const response = await api('/api/admin/telegram');
      const next = response.telegram || response;
      templates = normalizeTelegramTemplates(response.templates || next.templates);
      if (!templates['zh-CN'] || !templates['en-US']) {
        throw new Error('Telegram built-in templates are unavailable');
      }
      config = {
        bot_token: '',
        subscriptions: (next.subscriptions || []).map(subscription =>
          normalizeTelegramSubscription(subscription, templates),
        ),
      };
      tokenConfigured = Boolean(next.bot_token || next.token_configured);
      element('tg-bot-token').value = '';
      element('tg-bot-token').placeholder = tokenConfigured ? 'configured — leave blank to keep' : 'not configured';
      renderSubscriptions();
      if (editingIndex !== null) {
        if (config.subscriptions[editingIndex]) openEditor(editingIndex);
        else closeEditor();
      }
      return config;
    } catch (error) {
      element('tg-subscription-list').innerHTML = `<div class="empty">${escapeHTML(error.message)}</div>`;
      return config;
    }
  }

  function openEditor(index = null) {
    editingIndex = index;
    const subscription = index === null ? normalizeTelegramSubscription({}, templates) : config.subscriptions[index];
    element('tg-editor-title').textContent =
      index === null ? 'new subscription' : `edit subscription · ${subscription.id}`;
    element('tg-id').value = subscription.id;
    element('tg-id').disabled = index !== null;
    element('tg-name').value = subscription.name;
    element('tg-chat-id').value = subscription.chat_id;
    const language = element('tg-language');
    language.value = subscription.language;
    language.dataset.previousLanguage = subscription.language;
    element('tg-enabled').checked = subscription.enabled;
    element('tg-template').value = subscription.template || templates[subscription.language];
    element('tg-test-result').className = 'test-result feedback-in hidden';
    renderModelPicker(subscription.service_ids);
    showWindow('tg-editor');
  }

  function closeEditor() {
    editingIndex = null;
    hideWindow('tg-editor');
  }

  function collectSubscription() {
    const subscription = normalizeTelegramSubscription(
      {
        id: element('tg-id').value.trim(),
        name: element('tg-name').value.trim(),
        enabled: element('tg-enabled').checked,
        chat_id: element('tg-chat-id').value.trim(),
        language: element('tg-language').value,
        service_ids: selectedServiceIDs(),
        template: element('tg-template').value.trim(),
      },
      templates,
    );
    if (!subscription.id || !subscription.name || !subscription.chat_id || !subscription.template) {
      throw new Error('ID, name, chat ID, and template are required');
    }
    if (!subscription.service_ids.length) throw new Error('Select at least one model');
    const duplicateIndex = config.subscriptions.findIndex(item => item.id === subscription.id);
    if (duplicateIndex !== -1 && duplicateIndex !== editingIndex) {
      throw new Error('Subscription ID must be unique');
    }
    return subscription;
  }

  function applyEditor(options = {}) {
    const subscription = collectSubscription();
    if (editingIndex === null) {
      config.subscriptions.push(subscription);
      if (options.close === false) editingIndex = config.subscriptions.length - 1;
    } else {
      config.subscriptions[editingIndex] = subscription;
    }
    renderSubscriptions();
    if (options.close !== false) closeEditor();
    return subscription;
  }

  async function save(options = {}) {
    const tokenInput = element('tg-bot-token');
    const body = {
      bot_token: tokenInput.value.trim(),
      subscriptions: config.subscriptions,
    };
    await api('/api/admin/telegram', {
      method: 'PUT',
      body: JSON.stringify(body),
    });
    if (body.bot_token) tokenConfigured = true;
    tokenInput.value = '';
    tokenInput.placeholder = tokenConfigured ? 'configured — leave blank to keep' : 'not configured';
    if (!options.quiet) toast('Telegram config saved', 'success');
  }

  async function testSubscription(index, kind = 'event') {
    const subscription = config.subscriptions[index];
    if (!subscription) return;
    const rowResult = documentRef.querySelector(`[data-tg-test-status="${index}"]`);
    const editorResult = editingIndex === index ? element('tg-test-result') : null;
    const results = [rowResult, editorResult].filter(Boolean);
    await sendTelegramTest({ subscription, results, save, api, kind });
  }

  function deleteSubscription(index) {
    const subscription = config.subscriptions[index];
    if (!subscription || !confirmAction(`Delete subscription ${subscription.name || subscription.id}?`)) return;
    config.subscriptions.splice(index, 1);
    if (editingIndex === index) closeEditor();
    else if (editingIndex !== null && editingIndex > index) editingIndex--;
    renderSubscriptions();
    toast('Subscription removed from draft', 'info');
  }

  element('tg-new-btn').addEventListener('click', () => openEditor());
  element('tg-editor-cancel').addEventListener('click', closeEditor);
  element('tg-language').addEventListener('change', event => {
    const previousLanguage = event.target.dataset.previousLanguage || DEFAULT_TELEGRAM_LANGUAGE;
    const nextLanguage = event.target.value;
    const template = element('tg-template');
    if (!template.value.trim() || template.value.trim() === templates[previousLanguage].trim()) {
      template.value = templates[nextLanguage];
    }
    event.target.dataset.previousLanguage = nextLanguage;
  });
  element('tg-subscription-form').addEventListener('submit', event => {
    event.preventDefault();
    try {
      applyEditor();
      toast('Subscription applied to draft', 'success');
    } catch (error) {
      toast(error.message, 'error');
    }
  });
  element('tg-test-btn').addEventListener('click', async () => {
    try {
      const index = editingIndex === null ? config.subscriptions.length : editingIndex;
      applyEditor({ close: false });
      await testSubscription(index);
    } catch (error) {
      toast(error.message, 'error');
    }
  });
  element('tg-daily-test-btn').addEventListener('click', async () => {
    try {
      const index = editingIndex === null ? config.subscriptions.length : editingIndex;
      applyEditor({ close: false });
      await testSubscription(index, 'daily');
    } catch (error) {
      toast(error.message, 'error');
    }
  });
  element('tg-save-btn').addEventListener('click', async () => {
    try {
      if (!element('tg-editor').classList.contains('hidden')) applyEditor();
      await save();
    } catch (error) {
      toast(error.message, 'error');
    }
  });

  return {
    load,
    setServices,
    openEditor,
    closeEditor,
    applyEditor,
    save,
    testSubscription,
    renderSubscriptions,
    get config() {
      return {
        bot_token: config.bot_token,
        subscriptions: config.subscriptions.map(subscription => ({
          ...subscription,
          service_ids: subscription.service_ids.slice(),
        })),
      };
    },
  };
}
