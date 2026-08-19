export const UPDATE_TARGET_KEY = 'model_uptime_update_target';

export function renderUpdateStatus(documentRef, data) {
  const current = documentRef.getElementById('update-current');
  const latest = documentRef.getElementById('update-latest');
  const status = documentRef.getElementById('update-status');
  const detail = documentRef.getElementById('update-detail');
  const start = documentRef.getElementById('update-start-btn');
  current.textContent = data.current_version || 'dev';
  latest.textContent = data.latest_version || '—';
  status.className = '';

  if (data.updating) {
    status.textContent = 'Updating…';
    status.classList.add('warn');
  } else if (data.last_update_error) {
    status.textContent = 'Update failed';
    status.classList.add('bad');
  } else if (data.update_available) {
    status.textContent = 'Update available';
    status.classList.add('warn');
  } else {
    status.textContent = 'Up to date';
    status.classList.add('ok');
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
  status.textContent = restarting ? 'Restarting service…' : 'Check failed';
  status.className = restarting ? 'warn' : 'bad';
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
  const element = id => documentRef.getElementById(id);

  function clearPollTimer() {
    if (pollTimer !== null) cancel(pollTimer);
    pollTimer = null;
  }

  function render(data) {
    status = data;
    renderUpdateStatus(documentRef, data);
  }

  function poll(target) {
    clearPollTimer();
    const deadline = now() + 11 * 60 * 1000;
    const run = async () => {
      try {
        const data = await api('/api/admin/update');
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
        renderUpdateError(documentRef, 'Waiting for the updated container to become available.', true);
      }
      if (now() >= deadline) {
        storage.removeItem(UPDATE_TARGET_KEY);
        renderUpdateError(
          documentRef,
          `The service did not return with ${target} within 11 minutes. Check Docker logs on the host.`,
        );
        return;
      }
      pollTimer = schedule(run, 2000);
    };
    pollTimer = schedule(run, 1200);
  }

  async function load(force = false) {
    const check = element('update-check-btn');
    check.disabled = true;
    try {
      const data = await api(force ? '/api/admin/update/check' : '/api/admin/update', force ? { method: 'POST' } : {});
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
      check.disabled = false;
    }
  }

  async function startUpdate() {
    if (!status || !status.update_available || !status.latest_version) return;
    const target = status.latest_version;
    if (!confirmAction(`Update model-uptime from ${status.current_version} to ${target}?`)) return;
    element('update-start-btn').disabled = true;
    try {
      const result = await api('/api/admin/update', { method: 'POST' });
      const requestedTarget = result.target_version || target;
      storage.setItem(UPDATE_TARGET_KEY, requestedTarget);
      render({ ...status, updating: true, last_update_error: '' });
      toast('Update triggered');
      poll(requestedTarget);
    } catch (error) {
      renderUpdateError(documentRef, error.message);
      toast(error.message);
    }
  }

  element('update-check-btn').addEventListener('click', () => {
    void load(true);
  });
  element('update-start-btn').addEventListener('click', () => {
    void startUpdate();
  });

  return {
    load,
    poll,
    startUpdate,
    stop: clearPollTimer,
    get status() {
      return status;
    },
  };
}
