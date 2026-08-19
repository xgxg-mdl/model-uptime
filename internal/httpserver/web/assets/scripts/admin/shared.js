export function escapeHTML(value) {
  return String(value).replace(
    /[&<>"]/g,
    character =>
      ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
      })[character],
  );
}

export function prefersReducedMotion(windowRef = globalThis.window) {
  return windowRef?.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
}

export function revealPanel(element, windowRef = globalThis.window, block = 'nearest') {
  element.classList.remove('hidden');
  element.scrollIntoView({
    behavior: prefersReducedMotion(windowRef) ? 'auto' : 'smooth',
    block,
  });
}

export function createToast({
  document: documentRef,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
} = {}) {
  const element = documentRef.getElementById('toast');
  const tones = new Set(['info', 'success', 'warning', 'error']);
  let timer = null;
  return (message, tone = 'info') => {
    const resolvedTone = tones.has(tone) ? tone : 'info';
    element.setAttribute('aria-hidden', 'false');
    element.textContent = String(message ?? 'Something went wrong');
    element.classList.remove('info', 'success', 'warning', 'error');
    element.classList.add(resolvedTone, 'show');
    element.setAttribute('role', resolvedTone === 'error' ? 'alert' : 'status');
    element.setAttribute('aria-live', resolvedTone === 'error' ? 'assertive' : 'polite');
    if (timer !== null) cancel(timer);
    const duration = resolvedTone === 'error' ? 4200 : resolvedTone === 'warning' ? 3400 : 2800;
    timer = schedule(() => {
      element.classList.remove('show');
      element.setAttribute('aria-hidden', 'true');
      timer = null;
    }, duration);
  };
}

export function createAdminClient({ fetch: fetchImpl, token, onUnauthorized } = {}) {
  return async function request(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json');
    }
    headers.set('Authorization', `Bearer ${token()}`);
    const response = await fetchImpl(path, { ...options, headers });
    if (response.status === 401) {
      onUnauthorized();
      throw new Error('unauthorized');
    }
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    return data;
  };
}
