export function fillPageForm(documentRef, page) {
  documentRef.getElementById('p-title').value = page.title || '';
  documentRef.getElementById('p-subtitle').value = page.subtitle || '';
  documentRef.getElementById('p-comment').value = page.probe_comment || '';
  documentRef.getElementById('p-public-url').value = page.public_url || '';
  documentRef.getElementById('p-history').value = page.history_len || 60;
  documentRef.getElementById('p-refresh').value = page.refresh_sec || 5;
  documentRef.getElementById('p-command-animation').checked = page.enable_command_animation !== false;
  documentRef.getElementById('p-uptime').checked = Boolean(page.show_uptime);
  documentRef.getElementById('p-samples').checked = Boolean(page.show_samples);
  documentRef.getElementById('p-latency').checked = Boolean(page.show_latency);
  documentRef.getElementById('p-avload').checked = Boolean(page.show_avg_load);
}

export function readPageForm(documentRef) {
  return {
    title: documentRef.getElementById('p-title').value.trim(),
    subtitle: documentRef.getElementById('p-subtitle').value.trim(),
    probe_comment: documentRef.getElementById('p-comment').value.trim(),
    public_url: documentRef.getElementById('p-public-url').value.trim(),
    history_len: Number.parseInt(documentRef.getElementById('p-history').value, 10) || 60,
    refresh_sec: Number.parseInt(documentRef.getElementById('p-refresh').value, 10) || 5,
    enable_command_animation: documentRef.getElementById('p-command-animation').checked,
    show_uptime: documentRef.getElementById('p-uptime').checked,
    show_samples: documentRef.getElementById('p-samples').checked,
    show_latency: documentRef.getElementById('p-latency').checked,
    show_avg_load: documentRef.getElementById('p-avload').checked,
  };
}

export function createPageSettingsController({ document: documentRef, api, toast } = {}) {
  async function load() {
    try {
      const page = await api('/api/admin/page');
      fillPageForm(documentRef, page);
      return page;
    } catch (error) {
      toast(error.message);
      return null;
    }
  }

  async function save(event) {
    event.preventDefault();
    try {
      const page = await api('/api/admin/page', {
        method: 'PUT',
        body: JSON.stringify(readPageForm(documentRef)),
      });
      // Normalize/default rules live on the server; reflect its authoritative result.
      fillPageForm(documentRef, page);
      toast('显示配置已保存');
      return page;
    } catch (error) {
      toast(error.message);
      return null;
    }
  }

  documentRef.getElementById('page-form').addEventListener('submit', event => {
    void save(event);
  });
  return { load, save };
}
