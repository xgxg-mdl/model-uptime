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
  padding = VIEWPORT_PADDING,
}) {
  const maxX = Math.max(padding, viewportWidth - width - padding);
  const maxY = Math.max(padding, viewportHeight - height - padding);
  return {
    x: Math.min(Math.max(x, padding), maxX),
    y: Math.min(Math.max(y, padding), maxY),
  };
}

export function createWindowManager({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  schedule = globalThis.setTimeout,
} = {}) {
  const elements = new Map();
  const closeHandlers = new Map();
  const layer = documentRef.getElementById('window-layer');
  const dock = documentRef.getElementById('window-dock');
  let zIndex = 100;

  const windowElements = documentRef.querySelectorAll?.('[data-window-id]') || [];
  windowElements.forEach(element => {
    const id = element.dataset.windowId;
    elements.set(id, element);
    if (element.dataset.windowPopup !== undefined) layer?.append(element);
    bindWindow(element);
  });

  function visiblePopups() {
    return Array.from(elements.values()).filter(
      element => element.dataset.windowPopup !== undefined && !element.classList.contains('hidden'),
    );
  }

  function mainWindow() {
    return Array.from(elements.values()).find(element => element.dataset.windowMain !== undefined);
  }

  function focus(id) {
    const target = elements.get(id);
    if (!target || target.classList.contains('hidden')) return;
    elements.forEach(element => element.classList.remove('is-active'));
    target.classList.add('is-active');
    if (target.dataset.windowPopup !== undefined) target.style.zIndex = String(++zIndex);
  }

  function focusTopWindow() {
    const popups = visiblePopups().sort(
      (left, right) => Number(left.style.zIndex || 0) - Number(right.style.zIndex || 0),
    );
    const next = popups.at(-1) || mainWindow();
    if (next && !next.classList.contains('hidden')) focus(next.dataset.windowId);
  }

  function resetPosition(element) {
    for (const property of ['left', 'top', 'right', 'bottom', 'width', 'height', 'z-index']) {
      element.style.removeProperty(property);
    }
    element.style.removeProperty('transform');
  }

  function viewportSize() {
    return {
      width: documentRef.documentElement?.clientWidth || windowRef.innerWidth,
      height: documentRef.documentElement?.clientHeight || windowRef.innerHeight,
    };
  }

  function keepInViewport(element) {
    if (
      element.dataset.windowPopup === undefined ||
      element.classList.contains('hidden') ||
      element.classList.contains('is-maximized') ||
      !element.style.left
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
    });
    element.style.left = position.x + 'px';
    element.style.top = position.y + 'px';
  }

  function setMode(element, mode) {
    element.classList.toggle('is-minimized', mode === 'minimized');
    element.classList.toggle('is-maximized', mode === 'maximized');
    element.dataset.windowMode = mode;
  }

  function removeDockItem(id) {
    dock?.querySelector('[data-window-restore="' + id + '"]')?.remove();
  }

  function addDockItem(element) {
    if (!dock || dock.querySelector('[data-window-restore="' + element.dataset.windowId + '"]')) return;
    const button = documentRef.createElement('button');
    button.type = 'button';
    button.className = 'window-dock-item';
    button.dataset.windowRestore = element.dataset.windowId;
    button.textContent = element.dataset.windowDockLabel || element.dataset.windowId;
    button.addEventListener('click', () => open(element.dataset.windowId));
    dock.append(button);
  }

  function toggleMode(id, action) {
    const element = elements.get(id);
    if (!element) return;
    if (action === 'minimize') {
      setMode(element, 'minimized');
      element.classList.add('hidden');
      element.classList.remove('is-active');
      addDockItem(element);
      focusTopWindow();
      return;
    }
    focus(id);
    setMode(element, nextWindowMode(element.dataset.windowMode || 'normal', action));
  }

  function open(id) {
    const element = elements.get(id);
    if (!element) return;
    removeDockItem(id);
    resetPosition(element);
    setMode(element, 'normal');
    element.classList.remove('hidden');
    element.classList.remove('is-opening');
    void element.offsetWidth;
    element.classList.add('is-opening');
    focus(id);
    if (!prefersReducedMotion(windowRef)) {
      schedule(() => element.classList.remove('is-opening'), 260);
    } else {
      element.classList.remove('is-opening');
    }
  }

  function close(id) {
    const element = elements.get(id);
    if (!element) return;
    element.classList.add('hidden');
    element.classList.remove('is-active', 'is-opening', 'is-minimized', 'is-maximized');
    element.dataset.windowMode = 'normal';
    resetPosition(element);
    focusTopWindow();
  }

  function requestClose(id) {
    const handler = closeHandlers.get(id);
    if (handler) handler();
    else {
      const element = elements.get(id);
      close(id);
      if (element) addDockItem(element);
    }
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
    element.addEventListener('pointerdown', () => focus(id));
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
  if (initialWindow) focus(initialWindow.dataset.windowId);
  const handleViewportResize = () => elements.forEach(keepInViewport);
  windowRef?.addEventListener?.('resize', handleViewportResize);
  windowRef?.visualViewport?.addEventListener?.('resize', handleViewportResize);

  return {
    open,
    close,
    focus,
    requestClose,
    setCloseHandler,
    toggleMode,
  };
}
