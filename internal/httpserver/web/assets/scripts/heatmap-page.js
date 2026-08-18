const DEFAULT_RANGE = 'week';
const REFRESH_MS = 60_000;
const VALID_RANGES = new Set(['day', 'week', 'month']);

export function normalizeRange(value) {
  return VALID_RANGES.has(value) ? value : DEFAULT_RANGE;
}

function pad(value) {
  return String(value).padStart(2, '0');
}

export function formatBeijingTime(timestamp) {
  const date = new Date((Number(timestamp) + 8 * 60 * 60) * 1000);
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}`;
}

export function formatLatency(milliseconds) {
  const value = Number(milliseconds) || 0;
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s`;
  return `${value}ms`;
}

function createElement(documentRef, tagName, className = '', text) {
  const element = documentRef.createElement(tagName);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = String(text);
  return element;
}

function appendText(documentRef, parent, text) {
  parent.append(documentRef.createTextNode(String(text)));
}

function appendMetric(documentRef, parent, label, value, valueClass = '') {
  const metric = createElement(documentRef, 'span');
  appendText(documentRef, metric, `${label} `);
  metric.append(createElement(documentRef, 'b', valueClass, value));
  parent.append(metric);
}

function statusLabel(status) {
  return {
    healthy: 'HEALTHY',
    warning: 'WARNING',
    failing: 'FAILING',
    insufficient: 'INSUFFICIENT DATA',
    unobserved: 'NO DATA',
    pending: 'PENDING',
  }[status] || String(status).toUpperCase();
}

function statusTone(status) {
  if (status === 'healthy') return 'ok';
  if (status === 'warning') return 'warn';
  if (status === 'failing') return 'bad';
  return 'dim';
}

export function cellTooltipModel(cell) {
  const fields = [
    ['period', `${formatBeijingTime(cell.start_ts)} – ${formatBeijingTime(cell.end_ts)}`],
  ];
  if (cell.expected_samples > 0) {
    fields.push(['coverage', `${Number(cell.coverage_pct || 0).toFixed(1)}% (${cell.actual_samples}/${cell.expected_samples})`]);
  }
  if (cell.actual_samples > 0) {
    fields.push(
      ['healthy', `${cell.healthy_samples} (${percentage(cell.healthy_samples, cell.actual_samples)})`],
      ['warning', `${cell.warning_samples} (${percentage(cell.warning_samples, cell.actual_samples)})`],
      ['failed', `${cell.failed_samples} (${percentage(cell.failed_samples, cell.actual_samples)})`],
      ['uptime', `${Number(cell.uptime_pct || 0).toFixed(2)}%`],
    );
    if (cell.healthy_samples + cell.warning_samples > 0) {
      fields.push(['avg / p95', `${formatLatency(cell.avg_latency_ms)} / ${formatLatency(cell.p95_latency_ms)}`]);
    }
  }
  return { status: statusLabel(cell.status), statusClass: statusTone(cell.status), fields };
}

function percentage(count, total) {
  return `${(Number(count || 0) / Number(total || 1) * 100).toFixed(1)}%`;
}

function accessibleCellName(cell) {
  const tooltip = cellTooltipModel(cell);
  return [tooltip.status, ...tooltip.fields.map(([key, value]) => `${key} ${value}`)].join(', ');
}

function isDescendant(root, node) {
  for (let current = node; current; current = current.parentNode) {
    if (current === root) return true;
  }
  return false;
}

export function createHeatmapRenderer({
  document: documentRef,
  window: windowRef,
  scheduleFrame,
  cancelFrame,
} = {}) {
  if (!documentRef) throw new Error('document is required');
  const output = documentRef.getElementById('heatmap-out');
  const tip = documentRef.getElementById('tip');
  const cellModels = new WeakMap();
  const cellSignatures = new WeakMap();
  const schedule = scheduleFrame || (typeof windowRef?.requestAnimationFrame === 'function'
    ? callback => windowRef.requestAnimationFrame(callback)
    : callback => { callback(); return null; });
  const cancel = cancelFrame || (typeof windowRef?.cancelAnimationFrame === 'function'
    ? handle => windowRef.cancelAnimationFrame(handle)
    : () => {});
  let pendingFrame = null;
  let renderVersion = 0;
  let renderedLayoutKey = '';
  let renderedServiceKey = '';
  let renderedComplete = false;

  function cancelPendingRender() {
    if (pendingFrame !== null) cancel(pendingFrame);
    pendingFrame = null;
  }

  function hideTooltip() {
    tip.classList.remove('show');
  }

  function showTooltip(target, model) {
    tip.replaceChildren(createElement(documentRef, 'div', `t-status ${model.statusClass} bold`, model.status));
    for (const [key, value] of model.fields) {
      const row = createElement(documentRef, 'div');
      row.append(createElement(documentRef, 'span', 't-k', key));
      appendText(documentRef, row, ` ${value}`);
      tip.append(row);
    }
    tip.classList.add('show');
    const targetRect = target.getBoundingClientRect();
    const tipRect = tip.getBoundingClientRect();
    const viewportWidth = windowRef?.innerWidth || documentRef.documentElement?.clientWidth || 0;
    const halfWidth = tipRect.width / 2;
    const desiredLeft = targetRect.left + targetRect.width / 2;
    const minLeft = 8 + halfWidth;
    const maxLeft = Math.max(minLeft, viewportWidth - 8 - halfWidth);
    tip.style.left = `${Math.min(maxLeft, Math.max(minLeft, desiredLeft))}px`;
    tip.style.top = `${targetRect.top - 8}px`;
  }

  function cellSignature(cell) {
    return [
      cell.start_ts, cell.end_ts, cell.status, cell.intensity, cell.coverage_pct,
      cell.actual_samples, cell.expected_samples, cell.healthy_samples,
      cell.warning_samples, cell.failed_samples, cell.uptime_pct,
      cell.avg_latency_ms, cell.p95_latency_ms,
    ].join('|');
  }

  function updateCell(button, cell, serviceID, cellIndex) {
    button.setAttribute('data-service-id', serviceID);
    button.setAttribute('data-cell-index', String(cellIndex));
    cellModels.set(button, cell);
    const signature = cellSignature(cell);
    if (cellSignatures.get(button) === signature) return;
    const intensity = Math.min(5, Math.max(0, Number(cell.intensity) || 0));
    const intensityClass = intensity > 0 ? ` intensity-${intensity}` : '';
    button.className = `heat-cell ${cell.status}${intensityClass}`;
    button.setAttribute('aria-label', accessibleCellName(cell));
    cellSignatures.set(button, signature);
  }

  function createCell(cell, serviceID, cellIndex) {
    const button = createElement(documentRef, 'button');
    button.type = 'button';
    button.setAttribute('aria-describedby', 'tip');
    updateCell(button, cell, serviceID, cellIndex);
    return button;
  }

  function findCell(target) {
    for (let current = target; current && current !== output; current = current.parentNode) {
      if (current.classList?.contains('heat-cell')) return current;
    }
    return null;
  }

  function findGrid(cell) {
    for (let current = cell?.parentNode; current && current !== output; current = current.parentNode) {
      if (current.classList?.contains('heat-grid')) return current;
    }
    return null;
  }

  function setRovingCell(grid, activeCell) {
    for (const cell of grid?.querySelectorAll('.heat-cell') || []) {
      cell.setAttribute('tabindex', cell === activeCell ? '0' : '-1');
    }
  }

  function showCellTooltip(button) {
    const cell = cellModels.get(button);
    if (cell) showTooltip(button, cellTooltipModel(cell));
  }

  function handleGridKeydown(event, button) {
    const grid = findGrid(button);
    const cells = [...(grid?.querySelectorAll('.heat-cell') || [])];
    const index = cells.indexOf(button);
    if (!grid || index < 0) return;
    const columnCount = Number(grid.getAttribute('data-column-count')) || 24;
    const rowCount = Number(grid.getAttribute('data-row-count')) || 1;
    const row = Math.floor(index / columnCount);
    const column = index % columnCount;
    let nextIndex = index;
    switch (event.key) {
      case 'ArrowLeft':
        nextIndex = column > 0 ? index - 1 : index;
        break;
      case 'ArrowRight':
        nextIndex = column + 1 < columnCount && index + 1 < cells.length ? index + 1 : index;
        break;
      case 'ArrowUp':
        nextIndex = row > 0 ? index - columnCount : index;
        break;
      case 'ArrowDown':
        nextIndex = row + 1 < rowCount && index + columnCount < cells.length ? index + columnCount : index;
        break;
      case 'Home':
        nextIndex = row * columnCount;
        break;
      case 'End':
        nextIndex = Math.min(cells.length - 1, row * columnCount + columnCount - 1);
        break;
      case 'Escape':
        hideTooltip();
        button.blur();
        return;
      default:
        return;
    }
    event.preventDefault();
    const nextCell = cells[Math.min(cells.length - 1, Math.max(0, nextIndex))];
    setRovingCell(grid, nextCell);
    nextCell.focus();
  }

  output.addEventListener('mouseover', event => {
    const button = findCell(event.target);
    if (button) showCellTooltip(button);
  });
  output.addEventListener('mouseout', event => {
    const button = findCell(event.target);
    if (button && !isDescendant(button, event.relatedTarget)) hideTooltip();
  });
  output.addEventListener('focusin', event => {
    const button = findCell(event.target);
    if (!button) return;
    setRovingCell(findGrid(button), button);
    showCellTooltip(button);
  });
  output.addEventListener('focusout', event => {
    if (findCell(event.target)) hideTooltip();
  });
  output.addEventListener('click', event => {
    const button = findCell(event.target);
    if (button) showCellTooltip(button);
  });
  output.addEventListener('keydown', event => {
    const button = findCell(event.target);
    if (button) handleGridKeydown(event, button);
  });

  function configureGrid(grid, cells, rowCount, columnCount) {
    for (let index = 0; index < cells.length; index++) {
      cells[index].setAttribute('tabindex', index === 0 ? '0' : '-1');
    }
    grid.setAttribute('aria-rowcount', String(rowCount));
    grid.setAttribute('aria-colcount', String(columnCount + 1));
    grid.setAttribute('data-row-count', String(rowCount));
    grid.setAttribute('data-column-count', String(columnCount));
  }

  function createPanel(service, data) {
    const panel = createElement(documentRef, 'section', 'heatmap-panel');
    panel.setAttribute('data-service-id', service.id);
    const heading = createElement(documentRef, 'div', 'heatmap-panel-heading');
    const identity = createElement(documentRef, 'div', 'heatmap-model bold');
    identity.append(createElement(documentRef, 'span', 'heatmap-model-name', service.model));
    appendText(documentRef, identity, ' ');
    identity.append(createElement(documentRef, 'span', 'heatmap-provider', service.provider ? `· ${service.provider}` : ''));
    heading.append(identity, createElement(documentRef, 'span', `heatmap-current ${service.status}`, `● ${service.status}`));

    const summary = createElement(documentRef, 'div', 'heatmap-summary');
    appendMetric(documentRef, summary, 'uptime', service.samples ? `${Number(service.uptime_pct || 0).toFixed(2)}%` : '—', 'heatmap-uptime-value');
    appendMetric(documentRef, summary, 'p95', service.latency_samples ? formatLatency(service.p95_latency_ms) : '—', 'heatmap-p95-value');

    const axis = createElement(documentRef, 'div', 'heat-axis');
    axis.append(createElement(documentRef, 'span', '', data.range === 'day' ? 'min' : 'date'));
    for (const label of data.columns || []) axis.append(createElement(documentRef, 'span', '', label));

    const grid = createElement(documentRef, 'div', 'heat-grid');
    grid.setAttribute('role', 'grid');
    grid.setAttribute('aria-label', `${service.model} health history`);
    const columnCount = (data.columns || []).length || 24;
    const gridCells = [];
    for (let rowIndex = 0; rowIndex < (data.rows || []).length; rowIndex++) {
      const row = createElement(documentRef, 'div', 'heat-row');
      row.setAttribute('role', 'row');
      row.setAttribute('aria-rowindex', String(rowIndex + 1));
      const rowLabel = createElement(documentRef, 'span', 'heat-row-label', data.rows[rowIndex]);
      rowLabel.setAttribute('role', 'rowheader');
      rowLabel.setAttribute('aria-colindex', '1');
      row.append(rowLabel);
      const start = rowIndex * columnCount;
      for (const [columnIndex, cell] of service.cells.slice(start, start + columnCount).entries()) {
        const button = createCell(cell, service.id, start + columnIndex);
        button.setAttribute('role', 'gridcell');
        button.setAttribute('aria-colindex', String(columnIndex + 2));
        row.append(button);
        gridCells.push(button);
      }
      grid.append(row);
    }
    configureGrid(grid, gridCells, (data.rows || []).length, columnCount);
    panel.append(heading, summary, axis, grid);
    return panel;
  }

  function updatePanel(panel, service) {
    panel.setAttribute('data-service-id', service.id);
    panel.querySelector('.heatmap-model-name').textContent = service.model;
    panel.querySelector('.heatmap-provider').textContent = service.provider ? `· ${service.provider}` : '';
    const current = panel.querySelector('.heatmap-current');
    current.className = `heatmap-current ${service.status}`;
    current.textContent = `● ${service.status}`;
    panel.querySelector('.heatmap-uptime-value').textContent = service.samples
      ? `${Number(service.uptime_pct || 0).toFixed(2)}%`
      : '—';
    panel.querySelector('.heatmap-p95-value').textContent = service.latency_samples
      ? formatLatency(service.p95_latency_ms)
      : '—';
    const cells = [...panel.querySelectorAll('.heat-cell')];
    if (cells.length !== service.cells.length) return false;
    for (let index = 0; index < cells.length; index++) {
      updateCell(cells[index], service.cells[index], service.id, index);
    }
    return true;
  }

  function restoreFocusedCell(panel, focusedCell) {
    if (!focusedCell || panel.getAttribute('data-service-id') !== focusedCell.serviceID) return;
    const replacement = [...panel.querySelectorAll('.heat-cell')].find(cell => (
      cell.getAttribute('data-cell-index') === focusedCell.cellIndex
    ));
    replacement?.focus();
  }

  function render(data) {
    const version = ++renderVersion;
    cancelPendingRender();
    const activeElement = documentRef.activeElement;
    const focusedCell = activeElement?.classList?.contains('heat-cell') && isDescendant(output, activeElement)
      ? {
          serviceID: activeElement.getAttribute('data-service-id'),
          cellIndex: activeElement.getAttribute('data-cell-index'),
        }
      : null;
    hideTooltip();
    documentRef.title = `${data.page?.title || 'model-uptime'} // heatmap`;
    documentRef.getElementById('term-subtitle').textContent = data.page?.subtitle || 'model-uptime';
    documentRef.getElementById('active-range').textContent = data.range;
    documentRef.getElementById('updated').textContent = formatBeijingTime(data.generated_at).slice(11);
    for (const button of documentRef.querySelectorAll?.('[data-range]') || []) {
      const active = button.dataset.range === data.range;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
    }
    const services = data.services || [];
    const layoutKey = `${data.range}|${(data.rows || []).join(',')}|${(data.columns || []).join(',')}`;
    const serviceKey = JSON.stringify(services.map(service => service.id));
    const panels = [...output.querySelectorAll('.heatmap-panel')];
    if (renderedComplete && renderedLayoutKey === layoutKey && renderedServiceKey === serviceKey
      && panels.length === services.length
      && panels.every((panel, index) => updatePanel(panel, services[index]))) {
      return;
    }

    renderedLayoutKey = layoutKey;
    renderedServiceKey = serviceKey;
    renderedComplete = false;
    output.replaceChildren();
    if (!services.length) {
      output.append(createElement(documentRef, 'div', 'heatmap-empty', 'no enabled services'));
      renderedComplete = true;
      return;
    }

    let serviceIndex = 0;
    // 首个模型立即提交，避免其余模型的 DOM 构建阻塞首屏绘制。
    const appendNextPanel = () => {
      pendingFrame = null;
      if (version !== renderVersion) return;
      const panel = createPanel(services[serviceIndex], data);
      output.append(panel);
      restoreFocusedCell(panel, focusedCell);
      serviceIndex++;
      if (serviceIndex < services.length) {
        pendingFrame = schedule(appendNextPanel);
      } else {
        renderedComplete = true;
      }
    };
    appendNextPanel();
  }

  function renderError() {
    renderVersion++;
    cancelPendingRender();
    renderedLayoutKey = '';
    renderedServiceKey = '';
    renderedComplete = false;
    hideTooltip();
    output.replaceChildren(createElement(documentRef, 'div', 'heatmap-error', '● heatmap unavailable'));
  }

  return { render, renderError, hideTooltip };
}

export function createHeatmapPoller({
  fetchRange,
  render,
  renderError,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  refreshMS = REFRESH_MS,
  initialRange = DEFAULT_RANGE,
} = {}) {
  let active = false;
  let visible = true;
  let range = normalizeRange(initialRange);
  let timer = null;
  let sequence = 0;

  function cancelTimer() {
    if (timer !== null) cancel(timer);
    timer = null;
  }

  async function request() {
    const currentSequence = ++sequence;
    const requestedRange = range;
    try {
      const data = await fetchRange(requestedRange);
      if (!active || !visible || currentSequence !== sequence || requestedRange !== range) return;
      render(data);
    } catch (error) {
      if (!active || !visible || currentSequence !== sequence) return;
      renderError(error);
    } finally {
      if (active && visible && currentSequence === sequence) {
        timer = schedule(() => { void request(); }, refreshMS);
      }
    }
  }

  function start() {
    active = true;
    cancelTimer();
    if (visible) return request();
    return Promise.resolve();
  }

  function setRange(nextRange) {
    const normalized = normalizeRange(nextRange);
    if (normalized === range) return Promise.resolve();
    range = normalized;
    cancelTimer();
    sequence++;
    if (active && visible) return request();
    return Promise.resolve();
  }

  function setVisible(nextVisible) {
    visible = Boolean(nextVisible);
    cancelTimer();
    sequence++;
    if (active && visible) return request();
    return Promise.resolve();
  }

  function stop() {
    active = false;
    cancelTimer();
    sequence++;
  }

  return { start, setRange, setVisible, stop };
}

export function startHeatmapPage({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  fetch: fetchImpl = globalThis.fetch,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  now = () => Date.now(),
} = {}) {
  const url = new URL(windowRef.location.href);
  const initialRange = normalizeRange(url.searchParams.get('range'));
  documentRef.getElementById('login-time').textContent = formatBeijingTime(Math.floor(now() / 1000));
  const renderer = createHeatmapRenderer({ document: documentRef, window: windowRef });
  const poller = createHeatmapPoller({
    initialRange,
    schedule,
    cancel,
    async fetchRange(range) {
      const response = await fetchImpl(`/api/heatmap?range=${encodeURIComponent(range)}`, { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return response.json();
    },
    render: renderer.render,
    renderError: renderer.renderError,
  });
  for (const button of documentRef.querySelectorAll('[data-range]')) {
    button.addEventListener('click', () => {
      const range = normalizeRange(button.dataset.range);
      url.searchParams.set('range', range);
      windowRef.history.replaceState(null, '', url);
      void poller.setRange(range);
    });
  }
  documentRef.addEventListener('visibilitychange', () => {
    void poller.setVisible(documentRef.visibilityState !== 'hidden');
  });
  void poller.start();
  return poller;
}

if (typeof document !== 'undefined') startHeatmapPage();
