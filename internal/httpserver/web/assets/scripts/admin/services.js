import { escapeHTML, revealPanel } from './shared.js';

export const SERVICE_ACTIONS = [
  {
    id: 'edit',
    label: 'Edit service',
    destructive: false,
    icon: '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/>',
  },
  {
    id: 'copy',
    label: 'Duplicate service',
    destructive: false,
    icon: '<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  },
  {
    id: 'test',
    label: 'Test connection',
    destructive: false,
    icon: '<path d="m6 3 14 9-14 9Z"/>',
  },
  {
    id: 'del',
    label: 'Delete service',
    destructive: true,
    icon: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><path d="M10 11v6"/><path d="M14 11v6"/>',
  },
];

export function serviceActionButton(action, uid) {
  return `<button class="btn icon-btn${action.destructive ? ' bad' : ''}" type="button" data-act="${action.id}" data-uid="${uid}" title="${action.label}" aria-label="${action.label}">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${action.icon}</svg>
  </button>`;
}

export function serviceActionsMarkup(service) {
  const uid = escapeHTML(service.uid);
  const buttons = SERVICE_ACTIONS.map(action => serviceActionButton(action, uid)).join('');
  return `<div class="actions">${buttons}</div><div class="service-test-result feedback-in hidden" data-service-test-status="${uid}" role="status" aria-live="polite" aria-atomic="true"></div>`;
}

export function compareServiceOrder(left, right) {
  const orderDifference = (left.sort_order || Number.MAX_SAFE_INTEGER) - (right.sort_order || Number.MAX_SAFE_INTEGER);
  if (orderDifference) return orderDifference;
  return (
    String(left.name || '').localeCompare(String(right.name || '')) ||
    String(left.model || '').localeCompare(String(right.model || ''))
  );
}

export function renderServiceTableRow(service) {
  return `<tr data-service-row>
    <td><input type="checkbox" class="row-check" data-uid="${escapeHTML(service.uid)}" aria-label="select ${escapeHTML(service.name)}" /></td>
    <td class="service-primary"><b>${escapeHTML(service.name)}</b></td>
    <td>${escapeHTML(service.protocol)}</td>
    <td>${escapeHTML(service.model || '—')}</td>
    <td>${escapeHTML(service.provider || '—')}</td>
    <td>${escapeHTML(service.sort_order || '—')}</td>
    <td>${service.interval_sec || 60}s</td>
    <td><span class="dot ${service.enabled ? 'on' : 'off'}"></span>${service.enabled ? 'on' : 'off'}</td>
    <td class="service-actions">${serviceActionsMarkup(service)}</td>
  </tr>`;
}

export function renderServiceListItem(service) {
  const metadata = [service.protocol, service.model, service.provider, `order ${service.sort_order || '—'}`]
    .filter(Boolean)
    .join(' · ');
  return `<li class="service-item" data-service-row>
    <label class="service-item-select">
      <input type="checkbox" class="row-check" data-uid="${escapeHTML(service.uid)}" aria-label="select ${escapeHTML(service.name)}" />
    </label>
    <div class="service-item-title" title="${escapeHTML(service.name)}"><b>${escapeHTML(service.name)}</b></div>
    <div class="service-item-status"><span class="dot ${service.enabled ? 'on' : 'off'}"></span>${service.enabled ? 'on' : 'off'}</div>
    <div class="service-item-meta" title="${escapeHTML(metadata)}">${escapeHTML(metadata)}</div>
    <div class="service-item-interval">${service.interval_sec || 60}s</div>
    <div class="service-item-actions">${serviceActionsMarkup(service)}</div>
  </li>`;
}

/** Keeps late saves from mutating a subsequently opened editor session. */
export function createEditorSessionState() {
  let version = 0;
  let editingUID = null;
  let savingVersion = null;
  return {
    open(serviceUID) {
      version++;
      editingUID = serviceUID;
      return version;
    },
    close() {
      version++;
      editingUID = null;
      return version;
    },
    beginSave() {
      if (savingVersion === version) return null;
      savingVersion = version;
      return { version, serviceUID: editingUID };
    },
    finishSave(sessionVersion) {
      if (savingVersion === sessionVersion) savingVersion = null;
    },
    isCurrent(sessionVersion) {
      return version === sessionVersion;
    },
    get version() {
      return version;
    },
    get editingUID() {
      return editingUID;
    },
    get saving() {
      return savingVersion === version;
    },
  };
}

export async function showServiceTestResult({ api, uid, result, button }) {
  const baseClass = result.className.includes('service-test-result')
    ? 'service-test-result feedback-in'
    : 'test-result feedback-in';
  result.hidden = false;
  result.className = baseClass;
  result.textContent = 'probing…';
  button.disabled = true;
  try {
    const response = await api(`/api/admin/services/${encodeURIComponent(uid)}/test`, { method: 'POST' });
    result.className = `${baseClass} ${response.ok ? 'ok' : 'bad'}`;
    result.innerHTML = `${response.ok ? '✓ OK' : '✗ FAIL'} · <span class="mute">${response.latency_ms}ms</span>${response.error ? ` · ${escapeHTML(response.error)}` : ''}`;
    return response;
  } catch (error) {
    result.className = `${baseClass} bad`;
    result.textContent = error.message;
    return null;
  } finally {
    button.disabled = false;
  }
}

export function createServicesController({
  document: documentRef,
  window: windowRef = globalThis.window,
  windows,
  api,
  toast,
  confirm: confirmAction = globalThis.confirm,
  onServicesChanged = () => {},
  onServiceDeleted = async () => {},
} = {}) {
  const editorState = createEditorSessionState();
  let services = [];

  const element = id => documentRef.getElementById(id);
  const showWindow = id => {
    if (windows) windows.open(id);
    else revealPanel(element(id), windowRef);
  };
  const hideWindow = id => {
    if (windows) windows.close(id);
    else element(id).classList.add('hidden');
  };

  function selectedUIDs() {
    return Array.from(
      new Set(Array.from(documentRef.querySelectorAll('.row-check:checked')).map(checkbox => checkbox.dataset.uid)),
    );
  }

  function updateBulkBar() {
    const uids = selectedUIDs();
    const count = uids.length;
    element('bulk-count').textContent = `${count} selected`;
    ['bulk-enable', 'bulk-disable', 'bulk-settings'].forEach(id => {
      element(id).disabled = count === 0;
    });
    element('bulk-actions').classList.toggle('hidden', count === 0);
    const selectAll = element('select-all');
    selectAll.checked = services.length > 0 && count === services.length;
    selectAll.indeterminate = count > 0 && count < services.length;
    element('bulk-editor-count').textContent = count;
  }

  function mirrorSelection(checkbox) {
    documentRef.querySelectorAll('.row-check').forEach(peer => {
      if (peer.dataset.uid === checkbox.dataset.uid) peer.checked = checkbox.checked;
    });
    updateBulkBar();
  }

  function dispatchServiceAction(actionID, service, button) {
    if (actionID === 'edit') openEditor(service);
    else if (actionID === 'copy') duplicateService(service.uid);
    else if (actionID === 'test') {
      const result = button.closest('[data-service-row]').querySelector('[data-service-test-status]');
      void showServiceTestResult({ api, uid: service.uid, result, button });
    } else if (actionID === 'del') deleteService(service.uid);
  }

  function bindServiceControls(root, renderedServices) {
    root.querySelectorAll('button[data-act]').forEach(button => {
      button.addEventListener('click', () => {
        const service = renderedServices.find(item => item.uid === button.dataset.uid);
        if (service) dispatchServiceAction(button.dataset.act, service, button);
      });
    });
    root.querySelectorAll('.row-check').forEach(checkbox => {
      checkbox.addEventListener('change', () => mirrorSelection(checkbox));
    });
  }

  async function load() {
    const table = element('svc-table');
    const list = element('svc-list');
    try {
      const data = await api('/api/admin/services');
      services = (data.services || []).slice().sort(compareServiceOrder);
      if (editorState.editingUID && !services.some(service => service.uid === editorState.editingUID)) closeEditor();
      onServicesChanged(services);
      if (!services.length) {
        table.innerHTML = '<tr><td colspan="9" class="empty">no services yet — add one below</td></tr>';
        list.innerHTML = '<li class="empty">no services yet — add one above</li>';
        updateBulkBar();
        return services;
      }
      table.innerHTML = services.map(renderServiceTableRow).join('');
      list.innerHTML = services.map(renderServiceListItem).join('');
      bindServiceControls(table, services);
      bindServiceControls(list, services);
      element('select-all').checked = false;
      updateBulkBar();
      return services;
    } catch (error) {
      services = [];
      onServicesChanged(services);
      table.innerHTML = `<tr><td colspan="9" class="empty">${escapeHTML(error.message)}</td></tr>`;
      list.innerHTML = `<li class="empty">${escapeHTML(error.message)}</li>`;
      updateBulkBar();
      return services;
    }
  }

  async function bulkUpdate(uids, patch) {
    return api('/api/admin/services', {
      method: 'PATCH',
      body: JSON.stringify({ uids, patch }),
    });
  }

  async function bulkSetEnabled(enabled) {
    const uids = selectedUIDs();
    if (!uids.length) return;
    try {
      await bulkUpdate(uids, { enabled });
      toast(
        `${uids.length} ${uids.length === 1 ? 'service' : 'services'} ${enabled ? 'enabled' : 'disabled'}`,
        'success',
      );
      await load();
    } catch (error) {
      toast(error.message, 'error');
    }
  }

  async function applyBulkSettings(event) {
    event.preventDefault();
    const uids = selectedUIDs();
    if (!uids.length) {
      toast('Select at least one service', 'warning');
      return;
    }
    const patch = {};
    const interval = element('b-interval').value.trim();
    if (interval) patch.interval_sec = Number.parseInt(interval, 10);
    const timeout = element('b-timeout').value.trim();
    if (timeout) patch.timeout_sec = Number.parseInt(timeout, 10);
    const warning = element('b-warning').value.trim();
    if (warning) patch.warning_sec = Number.parseInt(warning, 10);
    const stream = element('b-stream').value;
    if (stream === 'true') patch.stream = true;
    else if (stream === 'false') patch.stream = false;
    if (!Object.keys(patch).length) {
      toast('Enter at least one setting to update', 'warning');
      return;
    }
    try {
      await bulkUpdate(uids, patch);
      toast(`Updated ${uids.length} ${uids.length === 1 ? 'service' : 'services'}`, 'success');
      closeBulkEditor();
      await load();
    } catch (error) {
      toast(error.message, 'error');
    }
  }

  async function deleteService(uid) {
    const service = services.find(item => item.uid === uid);
    if (!confirmAction(`删除服务 ${service?.model || service?.name || ''}？此操作不可恢复。`)) return;
    try {
      await api(`/api/admin/services/${encodeURIComponent(uid)}`, {
        method: 'DELETE',
      });
      if (editorState.editingUID === uid) closeEditor();
      toast('Service deleted', 'success');
      await Promise.all([load(), onServiceDeleted()]);
    } catch (error) {
      toast(error.message, 'error');
    }
  }

  async function duplicateService(uid) {
    try {
      await api(`/api/admin/services/${encodeURIComponent(uid)}/duplicate`, {
        method: 'POST',
      });
      toast('Service duplicated', 'success');
      await load();
    } catch (error) {
      toast(error.message, 'error');
    }
  }

  function showHttpFields(protocol) {
    documentRef.querySelectorAll('.llm').forEach(field => field.classList.toggle('hidden', protocol === 'http'));
    documentRef.querySelectorAll('.http').forEach(field => field.classList.toggle('hidden', protocol !== 'http'));
  }

  function beginEditor(serviceUID) {
    editorState.open(serviceUID);
    element('save-btn').disabled = false;
  }

  function closeEditor() {
    editorState.close();
    hideWindow('editor');
    element('test-result').className = 'test-result feedback-in hidden';
    element('save-btn').disabled = false;
  }

  function openEditor(service) {
    beginEditor(service.uid);
    element('editor-title').textContent = `edit service · ${service.model}`;
    element('f-name').value = service.name || '';
    element('f-provider').value = service.provider || '';
    element('f-sort-order').value = service.sort_order || '';
    element('f-protocol').value = service.protocol || 'chat';
    element('f-model').value = service.model || '';
    element('f-base').value = service.base_url || '';
    element('f-key').value = '';
    element('f-path').value = service.path || '';
    element('f-interval').value = service.interval_sec || 60;
    element('f-timeout').value = service.timeout_sec || 60;
    element('f-warning').value = service.warning_sec || 30;
    element('f-enabled').checked = service.enabled;
    element('f-stream').checked = service.stream !== false;
    element('f-method').value = (service.method || 'GET').toUpperCase();
    element('f-expect').value = service.expect_status || 200;
    element('f-headers').value = service.headers ? JSON.stringify(service.headers, null, 2) : '';
    element('f-body').value = service.body || '';
    showHttpFields(service.protocol);
    element('test-result').hidden = true;
    showWindow('editor');
  }

  function openNew() {
    beginEditor(null);
    element('editor-title').textContent = 'new service';
    ['f-name', 'f-provider', 'f-model', 'f-base', 'f-key', 'f-path', 'f-headers', 'f-body'].forEach(id => {
      element(id).value = '';
    });
    element('f-protocol').value = 'chat';
    element('f-interval').value = 60;
    element('f-timeout').value = 60;
    element('f-warning').value = 30;
    element('f-sort-order').value = '';
    element('f-enabled').checked = true;
    element('f-stream').checked = true;
    element('f-method').value = 'GET';
    element('f-expect').value = 200;
    showHttpFields('chat');
    element('test-result').hidden = true;
    showWindow('editor');
  }

  function collectService() {
    const headersRaw = element('f-headers').value.trim();
    let headers;
    if (headersRaw) {
      try {
        headers = JSON.parse(headersRaw);
      } catch {
        throw new Error('Headers 不是合法 JSON');
      }
    }
    const protocol = element('f-protocol').value;
    return {
      name: element('f-name').value.trim(),
      provider: element('f-provider').value.trim(),
      protocol,
      model: element('f-model').value.trim(),
      base_url: element('f-base').value.trim(),
      api_key: element('f-key').value,
      path: protocol === 'http' ? '' : element('f-path').value.trim(),
      sort_order: Number.parseInt(element('f-sort-order').value, 10) || 0,
      interval_sec: Number.parseInt(element('f-interval').value, 10) || 60,
      timeout_sec: Number.parseInt(element('f-timeout').value, 10) || 60,
      warning_sec: Number.parseInt(element('f-warning').value, 10) || 30,
      enabled: element('f-enabled').checked,
      stream: protocol === 'http' ? undefined : element('f-stream').checked,
      method: protocol === 'http' ? element('f-method').value : '',
      expect_status: protocol === 'http' ? Number.parseInt(element('f-expect').value, 10) || 200 : 0,
      headers: protocol === 'http' ? headers : undefined,
      body: protocol === 'http' ? element('f-body').value : '',
    };
  }

  async function saveService(event) {
    event.preventDefault();
    let service;
    try {
      service = collectService();
    } catch (error) {
      toast(error.message, 'error');
      return;
    }
    const session = editorState.beginSave();
    if (!session) return;
    const saveButton = element('save-btn');
    saveButton.disabled = true;
    try {
      if (session.serviceUID) {
        await api(`/api/admin/services/${encodeURIComponent(session.serviceUID)}`, {
          method: 'PUT',
          body: JSON.stringify(service),
        });
        toast('Service saved', 'success');
      } else {
        await api('/api/admin/services', {
          method: 'POST',
          body: JSON.stringify(service),
        });
        toast('Service created', 'success');
      }
      if (editorState.isCurrent(session.version)) closeEditor();
      await load();
    } catch (error) {
      toast(error.message, 'error');
    } finally {
      editorState.finishSave(session.version);
      if (editorState.isCurrent(session.version)) saveButton.disabled = false;
    }
  }

  element('select-all').addEventListener('change', event => {
    documentRef.querySelectorAll('.row-check').forEach(checkbox => {
      checkbox.checked = event.target.checked;
    });
    updateBulkBar();
  });
  element('bulk-enable').addEventListener('click', () => {
    void bulkSetEnabled(true);
  });
  element('bulk-disable').addEventListener('click', () => {
    void bulkSetEnabled(false);
  });
  element('bulk-settings').addEventListener('click', () => {
    ['b-interval', 'b-timeout', 'b-warning'].forEach(id => {
      element(id).value = '';
    });
    element('b-stream').value = '';
    element('bulk-editor-count').textContent = selectedUIDs().length;
    showWindow('bulk-editor');
  });
  function closeBulkEditor() {
    hideWindow('bulk-editor');
  }
  element('bulk-cancel').addEventListener('click', closeBulkEditor);
  element('bulk-form').addEventListener('submit', event => {
    void applyBulkSettings(event);
  });
  element('f-protocol').addEventListener('change', event => showHttpFields(event.target.value));
  element('new-btn').addEventListener('click', openNew);
  element('cancel-btn').addEventListener('click', closeEditor);
  element('test-btn').addEventListener('click', () => {
    const uid = editorState.editingUID;
    if (!uid) {
      toast('Save the service before testing', 'warning');
      return;
    }
    void showServiceTestResult({
      api,
      uid,
      result: element('test-result'),
      button: element('test-btn'),
    });
  });
  element('svc-form').addEventListener('submit', event => {
    void saveService(event);
  });

  return {
    load,
    openEditor,
    openNew,
    closeEditor,
    closeBulkEditor,
    saveService,
    selectedUIDs,
    updateBulkBar,
    get services() {
      return services.slice();
    },
    editorState,
  };
}
