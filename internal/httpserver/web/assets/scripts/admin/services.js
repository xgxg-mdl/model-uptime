import {
  escapeHTML,
  focusWithoutScroll,
  revealPanel,
  setButtonPending,
} from './shared.js';

export const SERVICE_ACTIONS = [
  {
    id: 'edit', label: 'Edit service', destructive: false,
    icon: '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/>',
  },
  {
    id: 'copy', label: 'Duplicate service', destructive: false,
    icon: '<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  },
  {
    id: 'test', label: 'Test connection', destructive: false,
    icon: '<path d="m6 3 14 9-14 9Z"/>',
  },
  {
    id: 'del', label: 'Delete service', destructive: true,
    icon: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><path d="M10 11v6"/><path d="M14 11v6"/>',
  },
];

export function serviceActionButton(action, id) {
  const disclosure = action.id === 'edit' ? ' aria-controls="editor" aria-expanded="false"' : '';
  return `<button class="btn icon-btn${action.destructive ? ' bad' : ''}" type="button" data-act="${action.id}" data-id="${id}" title="${action.label}" aria-label="${action.label}"${disclosure}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${action.icon}</svg>
  </button>`;
}

export function serviceActionsMarkup(service) {
  const id = escapeHTML(service.id);
  const buttons = SERVICE_ACTIONS.map(action => serviceActionButton(action, id)).join('');
  return `<div class="actions">${buttons}</div><div class="service-test-result feedback-in hidden" data-service-test-status="${id}" role="status" aria-live="polite"></div>`;
}

export function renderServiceTableRow(service) {
  return `<tr data-service-row>
    <td><input type="checkbox" class="row-check" data-id="${escapeHTML(service.id)}" aria-label="select ${escapeHTML(service.name)}" /></td>
    <td class="service-primary"><b>${escapeHTML(service.name)}</b> <span class="tag">#${escapeHTML(service.id)}</span></td>
    <td>${escapeHTML(service.protocol)}</td>
    <td>${escapeHTML(service.model || '—')}</td>
    <td>${escapeHTML(service.provider || '—')}</td>
    <td>${service.interval_sec || 60}s</td>
    <td><span class="dot ${service.enabled ? 'on' : 'off'}"></span>${service.enabled ? 'on' : 'off'}</td>
    <td class="service-actions">${serviceActionsMarkup(service)}</td>
  </tr>`;
}

export function renderServiceListItem(service) {
  const metadata = [service.protocol, service.model, service.provider].filter(Boolean).join(' · ');
  return `<li class="service-item" data-service-row>
    <label class="service-item-select">
      <input type="checkbox" class="row-check" data-id="${escapeHTML(service.id)}" aria-label="select ${escapeHTML(service.name)}" />
    </label>
    <div class="service-item-title" title="${escapeHTML(service.name)} #${escapeHTML(service.id)}"><b>${escapeHTML(service.name)}</b> <span class="service-item-id">#${escapeHTML(service.id)}</span></div>
    <div class="service-item-status"><span class="dot ${service.enabled ? 'on' : 'off'}"></span>${service.enabled ? 'on' : 'off'}</div>
    <div class="service-item-meta" title="${escapeHTML(metadata)}">${escapeHTML(metadata)}</div>
    <div class="service-item-interval">${service.interval_sec || 60}s</div>
    <div class="service-item-actions">${serviceActionsMarkup(service)}</div>
  </li>`;
}

/** Keeps late saves from mutating a subsequently opened editor session. */
export function createEditorSessionState() {
  let version = 0;
  let editingID = null;
  let savingVersion = null;
  return {
    open(serviceID) {
      version++;
      editingID = serviceID;
      return version;
    },
    close() {
      version++;
      editingID = null;
      return version;
    },
    beginSave() {
      if (savingVersion === version) return null;
      savingVersion = version;
      return { version, serviceID: editingID };
    },
    finishSave(sessionVersion) {
      if (savingVersion === sessionVersion) savingVersion = null;
    },
    isCurrent(sessionVersion) { return version === sessionVersion; },
    get version() { return version; },
    get editingID() { return editingID; },
    get saving() { return savingVersion === version; },
  };
}

export function setEditorSavePending(button, editorState, sessionVersion, pending) {
  if (!editorState.isCurrent(sessionVersion)) return false;
  button.disabled = pending;
  button.textContent = pending ? 'saving…' : 'save';
  button.setAttribute?.('aria-busy', String(pending));
  return true;
}

// 将服务写操作串行化，避免旧请求完成后以过期快照覆盖新状态。
export function createMutationQueue() {
  let tail = Promise.resolve();
  return task => {
    const next = tail.then(task, task);
    tail = next.catch(() => {});
    return next;
  };
}

export function selectEditorTrigger(tableCandidates, listCandidates, serviceID, viewportWidth) {
  const preferred = viewportWidth < 960 ? listCandidates : tableCandidates;
  return [...preferred, ...tableCandidates, ...listCandidates]
    .find(button => button.dataset.id === serviceID) || null;
}

export async function showServiceTestResult({ api, id, result, button }) {
  const baseClass = result.className.includes('service-test-result')
    ? 'service-test-result feedback-in'
    : 'test-result feedback-in';
  result.hidden = false;
  result.className = baseClass;
  result.textContent = 'probing…';
  button.disabled = true;
  button.setAttribute?.('aria-busy', 'true');
  try {
    const response = await api(`/api/admin/services/${encodeURIComponent(id)}/test`, { method: 'POST' });
    result.className = `${baseClass} ${response.ok ? 'ok' : 'bad'}`;
    result.innerHTML = `${response.ok ? '✓ OK' : '✗ FAIL'} · <span class="mute">${response.latency_ms}ms</span>${response.error ? ` · ${escapeHTML(response.error)}` : ''}`;
    return response;
  } catch (error) {
    result.className = `${baseClass} bad`;
    result.textContent = error.message;
    return null;
  } finally {
    button.disabled = false;
    button.setAttribute?.('aria-busy', 'false');
  }
}

export function createServicesController({
  document: documentRef,
  window: windowRef = globalThis.window,
  api,
  toast,
  confirm: confirmAction = globalThis.confirm,
  onServicesChanged = () => {},
  onServiceDeleted = async () => {},
} = {}) {
  const editorState = createEditorSessionState();
  let services = [];
  let editorReturnFocus = null;
  let bulkReturnFocus = null;
  let bulkUpdating = false;
  const enqueueMutation = createMutationQueue();
  let loadSequence = 0;

  const element = id => documentRef.getElementById(id);

  function selectedIDs() {
    return Array.from(new Set(
      Array.from(documentRef.querySelectorAll('.row-check:checked')).map(checkbox => checkbox.dataset.id),
    ));
  }

  function updateBulkBar() {
    const ids = selectedIDs();
    const count = ids.length;
    element('bulk-count').textContent = `${count} selected`;
    ['bulk-enable', 'bulk-disable', 'bulk-settings'].forEach(id => {
      element(id).disabled = count === 0 || bulkUpdating;
    });
    element('bulk-actions').classList.toggle('hidden', count === 0);
    const selectAll = element('select-all');
    selectAll.checked = services.length > 0 && count === services.length;
    selectAll.indeterminate = count > 0 && count < services.length;
    element('bulk-editor-count').textContent = count;
  }

  function mirrorSelection(checkbox) {
    documentRef.querySelectorAll('.row-check').forEach(peer => {
      if (peer.dataset.id === checkbox.dataset.id) peer.checked = checkbox.checked;
    });
    updateBulkBar();
  }

  function findEditorTrigger(serviceID, table = element('svc-table'), list = element('svc-list')) {
    const tableCandidates = [...table.querySelectorAll('button[data-act="edit"]')];
    const listCandidates = [...list.querySelectorAll('button[data-act="edit"]')];
    const viewportWidth = windowRef?.innerWidth || documentRef.documentElement?.clientWidth || 1024;
    return selectEditorTrigger(tableCandidates, listCandidates, serviceID, viewportWidth);
  }

  function refreshEditorReturnFocus(table, list) {
    if (!editorState.editingID) return;
    const candidates = [
      ...table.querySelectorAll('button[data-act="edit"]'),
      ...list.querySelectorAll('button[data-act="edit"]'),
    ];
    const replacement = findEditorTrigger(editorState.editingID, table, list);
    if (!replacement) return;
    editorReturnFocus = replacement;
    candidates.forEach(button => button.setAttribute(
      'aria-expanded',
      String(button.dataset.id === editorState.editingID),
    ));
  }

  function setEditorDisclosure(serviceID, expanded) {
    if (!serviceID) return;
    (documentRef.querySelectorAll?.('button[data-act="edit"]') || []).forEach(button => {
      if (button.dataset.id === serviceID) button.setAttribute('aria-expanded', String(expanded));
    });
  }

  function dispatchServiceAction(actionID, service, button) {
    if (actionID === 'edit') openEditor(service, button);
    else if (actionID === 'copy') duplicateService(service.id, button);
    else if (actionID === 'test') {
      const result = button.closest('[data-service-row]').querySelector('[data-service-test-status]');
      void showServiceTestResult({ api, id: service.id, result, button });
    } else if (actionID === 'del') deleteService(service.id, button);
  }

  function bindServiceControls(root, renderedServices) {
    root.querySelectorAll('button[data-act]').forEach(button => {
      button.addEventListener('click', () => {
        const service = renderedServices.find(item => item.id === button.dataset.id);
        if (service) dispatchServiceAction(button.dataset.act, service, button);
      });
    });
    root.querySelectorAll('.row-check').forEach(checkbox => {
      checkbox.addEventListener('change', () => mirrorSelection(checkbox));
    });
  }

  async function load() {
    const sequence = ++loadSequence;
    const table = element('svc-table');
    const list = element('svc-list');
    try {
      const data = await api('/api/admin/services');
      if (sequence !== loadSequence) return services;
      services = data.services || [];
      if (editorState.editingID && !services.some(service => service.id === editorState.editingID)) closeEditor();
      onServicesChanged(services);
      if (!services.length) {
        table.innerHTML = '<tr><td colspan="8" class="empty">no services yet — add one below</td></tr>';
        list.innerHTML = '<li class="empty">no services yet — add one above</li>';
        updateBulkBar();
        return services;
      }
      table.innerHTML = services.map(renderServiceTableRow).join('');
      list.innerHTML = services.map(renderServiceListItem).join('');
      bindServiceControls(table, services);
      bindServiceControls(list, services);
      refreshEditorReturnFocus(table, list);
      element('select-all').checked = false;
      updateBulkBar();
      return services;
    } catch (error) {
      if (sequence !== loadSequence) return services;
      services = [];
      onServicesChanged(services);
      table.innerHTML = `<tr><td colspan="8" class="empty">${escapeHTML(error.message)}</td></tr>`;
      list.innerHTML = `<li class="empty">${escapeHTML(error.message)}</li>`;
      updateBulkBar();
      return services;
    }
  }

  async function bulkUpdate(ids, patch) {
    return api('/api/admin/services', {
      method: 'PATCH',
      body: JSON.stringify({ ids, patch }),
    });
  }

  async function bulkSetEnabled(enabled, button) {
    const ids = selectedIDs();
    if (!ids.length || bulkUpdating) return;
    bulkUpdating = true;
    updateBulkBar();
    setButtonPending(button, true, enabled ? 'enabling…' : 'disabling…');
    try {
      await enqueueMutation(async () => {
        await bulkUpdate(ids, { enabled });
        toast(`${ids.length} service${ids.length === 1 ? '' : 's'} ${enabled ? 'enabled' : 'disabled'}.`);
        await load();
        focusWithoutScroll(element('select-all'));
      });
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(button, false);
      bulkUpdating = false;
      updateBulkBar();
    }
  }

  async function applyBulkSettings(event) {
    event.preventDefault();
    if (bulkUpdating) return;
    const ids = selectedIDs();
    if (!ids.length) { toast('Select at least one service.'); return; }
    const patch = {};
    const interval = element('b-interval').value.trim();
    if (interval) patch.interval_sec = Number.parseInt(interval, 10);
    const timeout = element('b-timeout').value.trim();
    if (timeout) patch.timeout_sec = Number.parseInt(timeout, 10);
    const stream = element('b-stream').value;
    if (stream === 'true') patch.stream = true;
    else if (stream === 'false') patch.stream = false;
    if (!Object.keys(patch).length) { toast('Complete at least one field to apply.'); return; }
    const button = element('bulk-apply');
    bulkUpdating = true;
    updateBulkBar();
    setButtonPending(button, true, 'applying…');
    try {
      await enqueueMutation(async () => {
        await bulkUpdate(ids, patch);
        toast(`${ids.length} service${ids.length === 1 ? '' : 's'} updated.`);
        closeBulkEditor(false);
        await load();
        focusWithoutScroll(element('select-all'));
      });
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(button, false);
      bulkUpdating = false;
      updateBulkBar();
    }
  }

  async function deleteService(id, button) {
    if (!confirmAction(`Delete service ${id}? This cannot be undone.`)) return;
    setButtonPending(button, true, null);
    try {
      await enqueueMutation(async () => {
        await api(`/api/admin/services/${encodeURIComponent(id)}`, { method: 'DELETE' });
        if (editorState.editingID === id) closeEditor(false);
        toast('Service deleted.');
        await Promise.all([load(), onServiceDeleted()]);
        focusWithoutScroll(element('new-btn'));
      });
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(button, false);
    }
  }

  async function duplicateService(id, button) {
    setButtonPending(button, true, null);
    try {
      await enqueueMutation(async () => {
        await api(`/api/admin/services/${encodeURIComponent(id)}/duplicate`, { method: 'POST' });
        toast('Service duplicated.');
        await load();
        focusWithoutScroll(element('new-btn'));
      });
    } catch (error) {
      toast(error.message);
    } finally {
      setButtonPending(button, false);
    }
  }

  function showHttpFields(protocol) {
    documentRef.querySelectorAll('.llm').forEach(field => field.classList.toggle('hidden', protocol === 'http'));
    documentRef.querySelectorAll('.http').forEach(field => field.classList.toggle('hidden', protocol !== 'http'));
  }

  function beginEditor(serviceID, returnFocus) {
    setEditorDisclosure(editorState.editingID, false);
    const version = editorState.open(serviceID);
    editorReturnFocus?.setAttribute?.('aria-expanded', 'false');
    editorReturnFocus = returnFocus || documentRef.activeElement || element('new-btn');
    editorReturnFocus?.setAttribute?.('aria-expanded', 'true');
    setEditorDisclosure(serviceID, true);
    setEditorSavePending(element('save-btn'), editorState, version, false);
  }

  function closeEditor(restoreFocus = true) {
    const serviceID = editorState.editingID;
    const returnFocus = serviceID
      ? findEditorTrigger(serviceID) || editorReturnFocus || element('new-btn')
      : editorReturnFocus || element('new-btn');
    setEditorDisclosure(serviceID, false);
    const version = editorState.close();
    element('editor').classList.add('hidden');
    element('test-result').className = 'test-result feedback-in hidden';
    setEditorSavePending(element('save-btn'), editorState, version, false);
    editorReturnFocus?.setAttribute?.('aria-expanded', 'false');
    if (restoreFocus) focusWithoutScroll(returnFocus);
    editorReturnFocus = null;
  }

  function openEditor(service, returnFocus = null) {
    beginEditor(service.id, returnFocus);
    element('editor-title').textContent = `edit service · ${service.id}`;
    element('f-id-input').value = service.id;
    element('f-id-input').disabled = true;
    element('f-name').value = service.name || '';
    element('f-provider').value = service.provider || '';
    element('f-protocol').value = service.protocol || 'chat';
    element('f-model').value = service.model || '';
    element('f-base').value = service.base_url || '';
    element('f-key').value = '';
    element('f-path').value = service.path || '';
    element('f-interval').value = service.interval_sec || 60;
    element('f-timeout').value = service.timeout_sec || 15;
    element('f-enabled').checked = service.enabled;
    element('f-stream').checked = service.stream !== false;
    element('f-method').value = (service.method || 'GET').toUpperCase();
    element('f-expect').value = service.expect_status || 200;
    element('f-headers').value = service.headers ? JSON.stringify(service.headers, null, 2) : '';
    element('f-body').value = service.body || '';
    showHttpFields(service.protocol);
    element('test-result').hidden = true;
    revealPanel(element('editor'), windowRef, 'nearest', element('f-name'));
  }

  function openNew(returnFocus = null) {
    beginEditor(null, returnFocus);
    element('editor-title').textContent = 'new service';
    element('f-id-input').value = '';
    element('f-id-input').disabled = false;
    ['f-name', 'f-provider', 'f-model', 'f-base', 'f-key', 'f-path', 'f-headers', 'f-body']
      .forEach(id => { element(id).value = ''; });
    element('f-protocol').value = 'chat';
    element('f-interval').value = 60;
    element('f-timeout').value = 15;
    element('f-enabled').checked = true;
    element('f-stream').checked = true;
    element('f-method').value = 'GET';
    element('f-expect').value = 200;
    showHttpFields('chat');
    element('test-result').hidden = true;
    revealPanel(element('editor'), windowRef, 'nearest', element('f-id-input'));
  }

  function closeBulkEditor(restoreFocus = true) {
    element('bulk-editor').classList.add('hidden');
    element('bulk-settings').setAttribute('aria-expanded', 'false');
    if (restoreFocus) focusWithoutScroll(bulkReturnFocus || element('bulk-settings'));
    bulkReturnFocus = null;
  }

  function collectService() {
    const headersRaw = element('f-headers').value.trim();
    let headers;
    if (headersRaw) {
      try { headers = JSON.parse(headersRaw); }
      catch { throw new Error('Headers must contain valid JSON.'); }
    }
    const protocol = element('f-protocol').value;
    return {
      id: element('f-id-input').value.trim(),
      name: element('f-name').value.trim(),
      provider: element('f-provider').value.trim(),
      protocol,
      model: protocol === 'http' ? '' : element('f-model').value.trim(),
      base_url: element('f-base').value.trim(),
      api_key: element('f-key').value,
      path: protocol === 'http' ? '' : element('f-path').value.trim(),
      interval_sec: Number.parseInt(element('f-interval').value, 10) || 60,
      timeout_sec: Number.parseInt(element('f-timeout').value, 10) || 15,
      enabled: element('f-enabled').checked,
      stream: protocol === 'http' ? undefined : element('f-stream').checked,
      method: protocol === 'http' ? element('f-method').value : '',
      expect_status: protocol === 'http' ? (Number.parseInt(element('f-expect').value, 10) || 200) : 0,
      headers: protocol === 'http' ? headers : undefined,
      body: protocol === 'http' ? element('f-body').value : '',
    };
  }

  async function saveService(event) {
    event.preventDefault();
    let service;
    try { service = collectService(); }
    catch (error) { toast(error.message); return; }
    const session = editorState.beginSave();
    if (!session) return;
    const saveButton = element('save-btn');
    setEditorSavePending(saveButton, editorState, session.version, true);
    try {
      await enqueueMutation(async () => {
        // 编辑器取消后可能仍在等待前一个 mutation；新会话已打开时丢弃旧草稿。
        if (!editorState.isCurrent(session.version)) return;
        if (session.serviceID) {
          await api(`/api/admin/services/${encodeURIComponent(session.serviceID)}`, {
            method: 'PUT', body: JSON.stringify(service),
          });
          toast('Service saved.');
        } else {
          await api('/api/admin/services', { method: 'POST', body: JSON.stringify(service) });
          toast('Service created.');
        }
        const shouldClose = editorState.isCurrent(session.version);
        if (shouldClose) closeEditor(false);
        await load();
        if (shouldClose) focusWithoutScroll(element('new-btn'));
      });
    } catch (error) {
      toast(error.message);
    } finally {
      editorState.finishSave(session.version);
      setEditorSavePending(saveButton, editorState, session.version, false);
    }
  }

  element('select-all').addEventListener('change', event => {
    documentRef.querySelectorAll('.row-check').forEach(checkbox => { checkbox.checked = event.target.checked; });
    updateBulkBar();
  });
  element('bulk-enable').addEventListener('click', event => { void bulkSetEnabled(true, event.currentTarget); });
  element('bulk-disable').addEventListener('click', event => { void bulkSetEnabled(false, event.currentTarget); });
  element('bulk-settings').addEventListener('click', () => {
    if (bulkUpdating) return;
    bulkReturnFocus = documentRef.activeElement || element('bulk-settings');
    ['b-interval', 'b-timeout'].forEach(id => { element(id).value = ''; });
    element('b-stream').value = '';
    element('bulk-editor-count').textContent = selectedIDs().length;
    element('bulk-settings').setAttribute('aria-expanded', 'true');
    revealPanel(element('bulk-editor'), windowRef, 'start', element('b-interval'));
  });
  element('bulk-cancel').addEventListener('click', () => closeBulkEditor());
  element('bulk-form').addEventListener('submit', event => { void applyBulkSettings(event); });
  element('f-protocol').addEventListener('change', event => showHttpFields(event.target.value));
  element('new-btn').addEventListener('click', event => openNew(event.currentTarget));
  element('cancel-btn').addEventListener('click', () => closeEditor());
  element('test-btn').addEventListener('click', () => {
    const id = editorState.editingID;
    if (!id) { toast('Save the service before testing it.'); return; }
    void showServiceTestResult({ api, id, result: element('test-result'), button: element('test-btn') });
  });
  element('svc-form').addEventListener('submit', event => { void saveService(event); });

  return {
    load,
    openEditor,
    openNew,
    closeEditor,
    saveService,
    selectedIDs,
    updateBulkBar,
    get services() { return services.slice(); },
    editorState,
  };
}
