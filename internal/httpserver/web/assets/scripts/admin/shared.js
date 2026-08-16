export function escapeHTML(value) {
  return String(value).replace(/[&<>"]/g, character => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
  })[character]);
}

export function prefersReducedMotion(windowRef = globalThis.window) {
  return windowRef?.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
}

export function focusWithoutScroll(element) {
  if (!element?.focus) return;
  try { element.focus({ preventScroll: true }); }
  catch { element.focus(); }
}

export function revealPanel(element, windowRef = globalThis.window, block = 'nearest', focusTarget = null) {
  element.classList.remove('hidden');
  element.scrollIntoView({
    behavior: prefersReducedMotion(windowRef) ? 'auto' : 'smooth',
    block,
  });
  focusWithoutScroll(focusTarget);
}

const pendingButtonStates = new WeakMap();

export function setButtonPending(button, pending, pendingLabel = 'working…') {
  if (pending) {
    if (!pendingButtonStates.has(button)) {
      pendingButtonStates.set(button, {
        disabled: button.disabled,
        label: button.textContent,
        replaceLabel: pendingLabel !== null,
      });
    }
    button.disabled = true;
    button.setAttribute?.('aria-busy', 'true');
    if (pendingLabel !== null) button.textContent = pendingLabel;
    return;
  }
  const previous = pendingButtonStates.get(button);
  if (!previous) return;
  button.disabled = previous.disabled;
  if (previous.replaceLabel) button.textContent = previous.label;
  button.setAttribute?.('aria-busy', 'false');
  pendingButtonStates.delete(button);
}

export function createToast({
  document: documentRef,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
} = {}) {
  const element = documentRef.getElementById('toast');
  let timer = null;
  return message => {
    element.textContent = message;
    element.classList.add('show');
    if (timer !== null) cancel(timer);
    timer = schedule(() => element.classList.remove('show'), 2600);
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
