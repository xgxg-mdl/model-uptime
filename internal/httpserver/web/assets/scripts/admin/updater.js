import { setButtonPending } from './shared.js';

export const UPDATE_TARGET_KEY = 'model_uptime_update_target';

function setStatusText(element, text, className) {
  if (element.textContent !== text) element.textContent = text;
  element.className = className;
}

export function renderUpdateStatus(documentRef, data) {
  const current = documentRef.getElementById('update-current');
  const latest = documentRef.getElementById('update-latest');
  const status = documentRef.getElementById('update-status');
  const detail = documentRef.getElementById('update-detail');
  const start = documentRef.getElementById('update-start-btn');
  current.textContent = data.current_version || 'dev';
  latest.textContent = data.latest_version || '—';

  if (data.updating) {
    setStatusText(status, 'Updating…', 'warn');
  } else if (data.last_update_error) {
    setStatusText(status, 'Update failed', 'bad');
  } else if (data.update_available) {
    setStatusText(status, 'Update available', 'warn');
  } else {
    setStatusText(status, 'Up to date', 'ok');
  }

  const notes = [];
  if (data.disabled_reason) notes.push(data.disabled_reason);
  if (data.last_update_error) notes.push(data.last_update_error);
  if (data.deployment_tag) notes.push(`Deployment tag: ${data.deployment_tag}`);
  if (data.checked_at) notes.push(`Checked: ${new Date(data.checked_at).toLocaleString()}`);
  detail.textContent = notes.join(' · ') || 'Release image verified and ready.';
  start.disabled = !data.enabled || !data.update_available || data.updating;
}

export function renderUpdateError(documentRef, message, restarting = false) {
  const status = documentRef.getElementById('update-status');
  setStatusText(status, restarting ? 'Restarting service…' : 'Check failed', restarting ? 'warn' : 'bad');
  documentRef.getElementById('update-detail').textContent = message;
  documentRef.getElementById('update-start-btn').disabled = true;
}

export function createUpdateController({
  document: documentRef,
  api,
  toast,
  storage,
  confirm: confirmAction = globalThis.confirm,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  now = () => Date.now(),
} = {}) {
  let status = null;
  let pollTimer = null;
  let pollGeneration = 0;
  const element = id => documentRef.getElementById(id);

  function clearPollTimer() {
    pollGeneration++;
    if (pollTimer !== null) cancel(pollTimer);
    pollTimer = null;
  }

  function render(data) {
    status = data;
    renderUpdateStatus(documentRef, data);
  }

  function poll(target) {
    clearPollTimer();
    // 每轮携带代际令牌，避免重启检查后旧请求再创建计时器。
    const generation = pollGeneration;
    const deadline = now() + 11 * 60 * 1000;
    const run = async () => {
      if (generation !== pollGeneration) return;
      try {
        const data = await api('/api/admin/update');
        if (generation !== pollGeneration) return;
        render(data);
        if (data.current_version === target) {
          storage.removeItem(UPDATE_TARGET_KEY);
          toast(`Updated to ${target}`);
          return;
        }
        if (data.last_update_error) {
          storage.removeItem(UPDATE_TARGET_KEY);
          return;
        }
      } catch {
        if (generation !== pollGeneration) return;
        renderUpdateError(documentRef, 'Waiting for the updated container to become available.', true);
      }
      if (generation !== pollGeneration) return;
      if (now() >= deadline) {
        storage.removeItem(UPDATE_TARGET_KEY);
        renderUpdateError(documentRef, `The service did not return with ${target} within 11 minutes. Check Docker logs on the host.`);
        return;
      }
      pollTimer = schedule(() => {
        pollTimer = null;
        return run();
      }, 2000);
    };
    pollTimer = schedule(() => {
      pollTimer = null;
      return run();
    }, 1200);
  }

  async function load(force = false) {
    clearPollTimer();
    const check = element('update-check-btn');
    setButtonPending(check, true, 'checking…');
    try {
      const data = await api(
        force ? '/api/admin/update/check' : '/api/admin/update',
        force ? { method: 'POST' } : {},
      );
      render(data);
      const target = storage.getItem(UPDATE_TARGET_KEY);
      if (target && data.current_version === target) {
        storage.removeItem(UPDATE_TARGET_KEY);
        toast(`Updated to ${target}`);
      } else if (target) {
        poll(target);
      }
      return data;
    } catch (error) {
      renderUpdateError(documentRef, error.message);
      return null;
    } finally {
      setButtonPending(check, false);
    }
  }

  async function startUpdate() {
    if (!status || !status.update_available || !status.latest_version) return;
    const target = status.latest_version;
    if (!confirmAction(`Update model-uptime from ${status.current_version} to ${target}?`)) return;
    const start = element('update-start-btn');
    setButtonPending(start, true, 'starting…');
    let failed = false;
    try {
      const result = await api('/api/admin/update', { method: 'POST' });
      const requestedTarget = result.target_version || target;
      storage.setItem(UPDATE_TARGET_KEY, requestedTarget);
      render({ ...status, updating: true, last_update_error: '' });
      toast('Update triggered');
      poll(requestedTarget);
    } catch (error) {
      failed = true;
      renderUpdateError(documentRef, error.message);
      toast(error.message);
    } finally {
      setButtonPending(start, false);
      if (failed || !status || status.updating || !status.update_available) start.disabled = true;
    }
  }

  element('update-check-btn').addEventListener('click', () => { void load(true); });
  element('update-start-btn').addEventListener('click', () => { void startUpdate(); });

  return {
    load,
    poll,
    startUpdate,
    stop: clearPollTimer,
    get status() { return status; },
  };
}
