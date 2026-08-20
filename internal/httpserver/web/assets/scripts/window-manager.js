const VIEWPORT_PADDING = 8;

function prefersReducedMotion(windowRef) {
  return windowRef?.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
}

export function nextWindowMode(mode, action) {
  if (action === 'minimize') return mode === 'minimized' ? 'normal' : 'minimized';
  if (action === 'maximize') return mode === 'maximized' ? 'normal' : 'maximized';
  return mode;
}

export function clampWindowPosition({
  x,
  y,
  width,
  height,
  viewportWidth,
  viewportHeight,
  viewportLeft = 0,
  viewportTop = 0,
  padding = VIEWPORT_PADDING,
}) {
  const minX = viewportLeft + padding;
  const minY = viewportTop + padding;
  const maxX = Math.max(minX, viewportLeft + viewportWidth - width - padding);
  const maxY = Math.max(minY, viewportTop + viewportHeight - height - padding);
  return {
    x: Math.min(Math.max(x, minX), maxX),
    y: Math.min(Math.max(y, minY), maxY),
  };
}

export function createWindowManager({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  schedule = globalThis.setTimeout,
} = {}) {
  const elements = new Map();
  const closeHandlers = new Map();
  const returnFocus = new Map();
  const lastWindowFocus = new Map();
  const layer = documentRef.getElementById('window-layer');
  const dock = documentRef.getElementById('window-dock');
  let zIndex = 100;

  const windowElements = documentRef.querySelectorAll?.('[data-window-id]') || [];
  windowElements.forEach(element => {
    const id = element.dataset.windowId;
    elements.set(id, element);
    if (element.dataset.windowPopup !== undefined) layer?.append(element);
    bindWindow(element);
    syncWindowState(element);
  });

  function visiblePopups() {
    return Array.from(elements.values()).filter(
      element => element.dataset.windowPopup !== undefined && !element.classList.contains('hidden'),
    );
  }

  function mainWindow() {
    return Array.from(elements.values()).find(element => element.dataset.windowMain !== undefined);
  }

  function windowLabel(element) {
    const labelledBy = element.getAttribute?.('aria-labelledby');
    const labelledElement = labelledBy ? documentRef.getElementById(labelledBy) : null;
    return (
      labelledElement?.textContent?.trim() ||
      element.dataset.windowDockLabel ||
      element.dataset.windowId ||
      'application'
    );
  }

  function controlLabel(action, element, mode) {
    const label = windowLabel(element);
    if (action === 'maximize' && mode === 'maximized') return `Restore ${label} window`;
    return `${action[0].toUpperCase()}${action.slice(1)} ${label} window`;
  }

  function syncWindowState(element) {
    const mode = element.dataset.windowMode || 'normal';
    const hidden = element.classList.contains('hidden') || mode === 'minimized' || mode === 'closed';
    element.classList.toggle('is-minimized', mode === 'minimized');
    element.classList.toggle('is-maximized', mode === 'maximized');
    element.setAttribute?.('aria-hidden', String(hidden));
    element.querySelectorAll('[data-window-action]').forEach(control => {
      const action = control.dataset.windowAction;
      const label = controlLabel(action, element, mode);
      control.setAttribute('aria-label', label);
      control.setAttribute('title', label);
      if (action === 'maximize') control.setAttribute('aria-pressed', String(mode === 'maximized'));
      control.classList.toggle('is-restore', action === 'maximize' && mode === 'maximized');
    });
  }

  function setMode(element, mode) {
    element.dataset.windowMode = mode;
    syncWindowState(element);
  }

  function activate(id) {
    const target = elements.get(id);
    if (!target || target.classList.contains('hidden')) return;
    elements.forEach(element => element.classList.remove('is-active'));
    target.classList.add('is-active');
    if (target.dataset.windowPopup !== undefined) target.style.zIndex = String(++zIndex);
  }

  function isHidden(element) {
    for (let current = element; current; current = current.parentNode) {
      if (current.hidden || current.classList?.contains('hidden')) return true;
    }
    return false;
  }

  function isFocusable(element) {
    if (!element || typeof element.focus !== 'function' || element.disabled || isHidden(element)) return false;
    if ('isConnected' in element && !element.isConnected) return false;
    if (element.getAttribute?.('tabindex') === '-1') return false;
    if (element.tagName === 'INPUT' && element.getAttribute?.('type') === 'hidden') return false;
    if (['BUTTON', 'INPUT', 'SELECT', 'TEXTAREA'].includes(element.tagName)) return true;
    if (element.tagName === 'A' && element.getAttribute?.('href')) return true;
    return element.getAttribute?.('tabindex') !== null;
  }

  function focusableWindowElements(element) {
    return Array.from(element.querySelectorAll('*')).filter(isFocusable);
  }

  function focusWindowContents(element, preferred) {
    const initialTarget = element.querySelector('[data-window-initial-focus]');
    const focusableElements = focusableWindowElements(element);
    const target =
      (isFocusable(preferred) && element.contains?.(preferred) && preferred) ||
      (isFocusable(initialTarget) && initialTarget) ||
      focusableElements.find(candidate => isFocusable(candidate) && !candidate.closest?.('[data-window-titlebar]')) ||
      focusableElements[0];
    target?.focus();
    return target || null;
  }

  function focusTopWindow({ moveDOMFocus = false, fallback = null } = {}) {
    const popups = visiblePopups().sort(
      (left, right) => Number(left.style.zIndex || 0) - Number(right.style.zIndex || 0),
    );
    const next = popups.at(-1) || mainWindow();
    if (next && !next.classList.contains('hidden')) {
      activate(next.dataset.windowId);
      if (moveDOMFocus) focusWindowContents(next, lastWindowFocus.get(next.dataset.windowId));
      return;
    }
    if (moveDOMFocus && isFocusable(fallback)) fallback.focus();
  }

  function resetPosition(element) {
    for (const property of ['left', 'top', 'right', 'bottom', 'width', 'height', 'z-index']) {
      element.style.removeProperty(property);
    }
    element.style.removeProperty('transform');
  }

  function viewportSize() {
    const visualViewport = windowRef?.visualViewport;
    return {
      width: visualViewport?.width || documentRef.documentElement?.clientWidth || windowRef.innerWidth,
      height: visualViewport?.height || documentRef.documentElement?.clientHeight || windowRef.innerHeight,
      left: visualViewport?.offsetLeft || 0,
      top: visualViewport?.offsetTop || 0,
    };
  }

  function keepInViewport(element) {
    if (
      element.dataset.windowPopup === undefined ||
      element.classList.contains('hidden') ||
      element.classList.contains('is-maximized')
    ) {
      return;
    }
    const rect = element.getBoundingClientRect();
    const viewport = viewportSize();
    const position = clampWindowPosition({
      x: rect.left,
      y: rect.top,
      width: rect.width,
      height: rect.height,
      viewportWidth: viewport.width,
      viewportHeight: viewport.height,
      viewportLeft: viewport.left,
      viewportTop: viewport.top,
    });
    element.style.left = position.x + 'px';
    element.style.top = position.y + 'px';
    element.style.transform = 'none';
  }

  function removeDockItem(id) {
    dock?.querySelector('[data-window-restore="' + id + '"]')?.remove();
  }

  function createMinimizedPreview() {
    const preview = documentRef.createElement('span');
    preview.className = 'window-dock-preview';
    preview.setAttribute('aria-hidden', 'true');
    const titlebar = documentRef.createElement('span');
    titlebar.className = 'window-dock-preview-titlebar';
    for (let index = 0; index < 3; index += 1) titlebar.append(documentRef.createElement('i'));
    const body = documentRef.createElement('span');
    body.className = 'window-dock-preview-body';
    body.append(documentRef.createElement('i'), documentRef.createElement('i'));
    preview.append(titlebar, body);
    return preview;
  }

  function addDockItem(element, kind) {
    if (!dock) return null;
    removeDockItem(element.dataset.windowId);
    const label = windowLabel(element);
    const button = documentRef.createElement('button');
    button.type = 'button';
    button.className = `window-dock-item is-${kind}`;
    button.dataset.windowRestore = element.dataset.windowId;
    button.setAttribute('aria-label', `${kind === 'closed' ? 'Open' : 'Restore'} ${label} window`);
    button.setAttribute('title', label);

    if (kind === 'minimized') button.append(createMinimizedPreview());
    else {
      const mark = documentRef.createElement('span');
      mark.className = 'window-dock-app-mark';
      mark.setAttribute('aria-hidden', 'true');
      mark.textContent = '>_';
      button.append(mark);
    }
    const text = documentRef.createElement('span');
    text.className = 'window-dock-label';
    text.textContent = label;
    button.append(text);
    button.addEventListener('click', () =>
      kind === 'closed' ? open(element.dataset.windowId) : restore(element.dataset.windowId),
    );
    dock.append(button);
    return button;
  }

  function playOpenAnimation(element) {
    element.classList.remove('is-opening');
    void element.offsetWidth;
    element.classList.add('is-opening');
    if (!prefersReducedMotion(windowRef)) schedule(() => element.classList.remove('is-opening'), 260);
    else element.classList.remove('is-opening');
  }

  function rememberReturnFocus(element) {
    const activeElement = documentRef.activeElement;
    if (activeElement && !element.contains?.(activeElement)) returnFocus.set(element.dataset.windowId, activeElement);
  }

  function restoreReturnFocus(id) {
    const target = returnFocus.get(id);
    returnFocus.delete(id);
    if (!isFocusable(target)) return false;
    target.focus();
    return true;
  }

  function restore(id) {
    const element = elements.get(id);
    if (!element || element.dataset.windowMode !== 'minimized') return;
    removeDockItem(id);
    const restoreMode = element.dataset.windowRestoreMode || 'normal';
    delete element.dataset.windowRestoreMode;
    element.classList.remove('hidden');
    setMode(element, restoreMode);
    playOpenAnimation(element);
    activate(id);
    focusWindowContents(element, lastWindowFocus.get(id));
  }

  function toggleMode(id, action) {
    const element = elements.get(id);
    if (!element) return;
    if (action === 'minimize') {
      if (element.dataset.windowMode === 'minimized') {
        restore(id);
        return;
      }
      element.dataset.windowRestoreMode = element.dataset.windowMode === 'maximized' ? 'maximized' : 'normal';
      setMode(element, 'minimized');
      element.classList.add('hidden');
      element.classList.remove('is-active', 'is-opening');
      const dockItem = addDockItem(element, 'minimized');
      focusTopWindow({ moveDOMFocus: true, fallback: dockItem });
      return;
    }
    activate(id);
    setMode(element, nextWindowMode(element.dataset.windowMode || 'normal', action));
  }

  function open(id) {
    const element = elements.get(id);
    if (!element) return;
    if (element.dataset.windowMode === 'minimized') {
      restore(id);
      return;
    }
    if (!element.classList.contains('hidden') && element.dataset.windowMode !== 'closed') {
      activate(id);
      return;
    }
    rememberReturnFocus(element);
    removeDockItem(id);
    resetPosition(element);
    element.classList.remove('hidden');
    setMode(element, 'normal');
    playOpenAnimation(element);
    activate(id);
    focusWindowContents(element);
  }

  function close(id) {
    const element = elements.get(id);
    if (!element) return;
    const focusedInside = element.contains?.(documentRef.activeElement) === true;
    removeDockItem(id);
    element.classList.add('hidden');
    element.classList.remove('is-active', 'is-opening');
    delete element.dataset.windowRestoreMode;
    setMode(element, 'closed');
    resetPosition(element);
    const restoredFocus = restoreReturnFocus(id);
    focusTopWindow({ moveDOMFocus: focusedInside && !restoredFocus });
  }

  function requestClose(id) {
    const handler = closeHandlers.get(id);
    if (handler) {
      handler();
      return;
    }
    const element = elements.get(id);
    close(id);
    if (element?.dataset.windowMain !== undefined) addDockItem(element, 'closed')?.focus();
  }

  function setCloseHandler(id, handler) {
    closeHandlers.set(id, handler);
  }

  function beginDrag(event, element) {
    if (element.dataset.windowPopup === undefined || element.classList.contains('is-maximized')) return;
    if (event.target.closest('[data-window-action]')) return;
    if (event.button !== undefined && event.button !== 0) return;

    const titlebar = event.currentTarget;
    const rect = element.getBoundingClientRect();
    const origin = { pointerX: event.clientX, pointerY: event.clientY, x: rect.left, y: rect.top };
    element.classList.remove('is-opening');
    element.style.left = rect.left + 'px';
    element.style.top = rect.top + 'px';
    element.style.transform = 'none';
    titlebar.setPointerCapture?.(event.pointerId);
    titlebar.classList.add('is-dragging');
    event.preventDefault();

    const move = moveEvent => {
      const viewport = viewportSize();
      const position = clampWindowPosition({
        x: origin.x + moveEvent.clientX - origin.pointerX,
        y: origin.y + moveEvent.clientY - origin.pointerY,
        width: rect.width,
        height: rect.height,
        viewportWidth: viewport.width,
        viewportHeight: viewport.height,
        viewportLeft: viewport.left,
        viewportTop: viewport.top,
      });
      element.style.left = position.x + 'px';
      element.style.top = position.y + 'px';
    };
    const finish = finishEvent => {
      titlebar.releasePointerCapture?.(finishEvent.pointerId);
      titlebar.classList.remove('is-dragging');
      titlebar.removeEventListener('pointermove', move);
      titlebar.removeEventListener('pointerup', finish);
      titlebar.removeEventListener('pointercancel', finish);
    };
    titlebar.addEventListener('pointermove', move);
    titlebar.addEventListener('pointerup', finish);
    titlebar.addEventListener('pointercancel', finish);
  }

  function bindWindow(element) {
    const id = element.dataset.windowId;
    const titlebar = element.querySelector('[data-window-titlebar]');
    element.addEventListener('pointerdown', () => activate(id));
    element.addEventListener('focusin', event => {
      activate(id);
      if (isFocusable(event.target) && !event.target.closest?.('[data-window-titlebar]')) {
        lastWindowFocus.set(id, event.target);
      }
    });
    titlebar?.addEventListener('pointerdown', event => beginDrag(event, element));
    titlebar?.addEventListener('dblclick', event => {
      if (!event.target.closest('[data-window-action]')) toggleMode(id, 'maximize');
    });
    element.querySelectorAll('[data-window-action]').forEach(control => {
      control.addEventListener('click', event => {
        event.stopPropagation();
        const action = control.dataset.windowAction;
        if (action === 'close') requestClose(id);
        else toggleMode(id, action);
      });
    });
  }

  const initialWindow = mainWindow();
  if (initialWindow && !initialWindow.classList.contains('hidden')) activate(initialWindow.dataset.windowId);
  const handleViewportResize = () => elements.forEach(keepInViewport);
  windowRef?.addEventListener?.('resize', handleViewportResize);
  windowRef?.visualViewport?.addEventListener?.('resize', handleViewportResize);
  documentRef.addEventListener?.('keydown', event => {
    const activePopup = visiblePopups().find(element => element.classList.contains('is-active'));
    if (!activePopup) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      requestClose(activePopup.dataset.windowId);
      return;
    }
    if (event.key !== 'Tab' || activePopup.getAttribute?.('aria-modal') !== 'true') return;
    const focusableElements = focusableWindowElements(activePopup);
    if (!focusableElements.length) {
      event.preventDefault();
      return;
    }
    const first = focusableElements[0];
    const last = focusableElements.at(-1);
    if (event.shiftKey && (documentRef.activeElement === first || !activePopup.contains?.(documentRef.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (
      !event.shiftKey &&
      (documentRef.activeElement === last || !activePopup.contains?.(documentRef.activeElement))
    ) {
      event.preventDefault();
      first.focus();
    }
  });

  return {
    open,
    close,
    focus: activate,
    requestClose,
    restore,
    setCloseHandler,
    toggleMode,
  };
}
