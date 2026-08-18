const DEFAULT_RANGE = '7d';
const REFRESH_MS = 60_000;
const VALID_RANGES = new Set(['1d', '7d', '30d']);
const RANGE_LABELS = { '1d': '1d', '7d': '7d', '30d': '30d' };
const HOUR_AXIS_LABELS = ['00:00', '06:00', '12:00', '18:00', '24:00'];
const CELLS_PER_FRAME = 96;

export function normalizeRange(value) {
  return VALID_RANGES.has(value) ? value : DEFAULT_RANGE;
}

export function rangeLabel(value) {
  return RANGE_LABELS[normalizeRange(value)];
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

function currentStatusLabel(status) {
  return { healthy: 'online', warning: 'slow', failing: 'failing', pending: 'pending' }[status] || status;
}

export function gridLayout(range, rows = [], columns = [], cells = []) {
  const sourceColumnCount = columns.length || 24;
  if (range === '30d') {
    return {
      columnCount: rows.length,
      rows: Array.from({ length: sourceColumnCount }, (_, columnIndex) => (
        rows.map((_, rowIndex) => {
          const sourceIndex = rowIndex * sourceColumnCount + columnIndex;
          return { cell: cells[sourceIndex], sourceIndex };
        })
      )),
    };
  }
  return {
    columnCount: sourceColumnCount,
    rows: rows.map((_, rowIndex) => (
      cells.slice(rowIndex * sourceColumnCount, (rowIndex + 1) * sourceColumnCount)
        .map((cell, columnIndex) => ({ cell, sourceIndex: rowIndex * sourceColumnCount + columnIndex }))
    )),
  };
}

export function axisLabels(range, rows = []) {
  if (range !== '30d' || rows.length < 2) return [...HOUR_AXIS_LABELS];
  const lastIndex = rows.length - 1;
  return [0, 0.25, 0.5, 0.75, 1].map(position => rows[Math.round(lastIndex * position)]);
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
  const cellPeriodLabels = new Map();
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

  function cachedAccessibleCellName(cell) {
    const key = `${cell.start_ts}|${cell.end_ts}`;
    let period = cellPeriodLabels.get(key);
    if (!period) {
      period = `${formatBeijingTime(cell.start_ts)} – ${formatBeijingTime(cell.end_ts)}`;
      cellPeriodLabels.set(key, period);
    }
    return `${statusLabel(cell.status)}, ${period}`;
  }

  function updateCell(button, cell) {
    cellModels.set(button, cell);
    const signature = cellSignature(cell);
    if (cellSignatures.get(button) === signature) return;
    const intensity = Math.min(5, Math.max(0, Number(cell.intensity) || 0));
    const intensityClass = intensity > 0 ? ` intensity-${intensity}` : '';
    button.className = `heat-cell ${cell.status}${intensityClass}`;
    button.setAttribute('aria-label', cachedAccessibleCellName(cell));
    cellSignatures.set(button, signature);
  }

  function createCell(cell, serviceID, cellIndex) {
    const button = createElement(documentRef, 'button');
    button.type = 'button';
    button.setAttribute('aria-describedby', 'tip');
    button.setAttribute('data-service-id', serviceID);
    button.setAttribute('data-cell-index', String(cellIndex));
    updateCell(button, cell);
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
    grid.setAttribute('aria-colcount', String(columnCount));
    grid.setAttribute('data-row-count', String(rowCount));
    grid.setAttribute('data-column-count', String(columnCount));
  }

  function createPanel(service, data) {
    const panel = createElement(documentRef, 'section', 'heatmap-panel');
    panel.setAttribute('data-service-id', service.id);
    const heading = createElement(documentRef, 'div', 'line service-heading heatmap-panel-heading');
    heading.append(createElement(documentRef, 'span', 'mute', '→'));
    appendText(documentRef, heading, ' ');
    heading.append(createElement(documentRef, 'span', 'cmd bold heatmap-model-name', service.model));
    heading.append(createElement(documentRef, 'span', 'heatmap-provider', service.provider ? ` · ${service.provider}` : ''));
    heading.append(createElement(documentRef, 'span', `heatmap-current ${service.status}`, ` · ● ${currentStatusLabel(service.status)}`));

    const summary = createElement(documentRef, 'div', 'svc-meta service-indent heatmap-summary');
    appendMetric(documentRef, summary, 'uptime', service.samples ? `${Number(service.uptime_pct || 0).toFixed(2)}%` : '—', 'heatmap-uptime-value');
    appendMetric(documentRef, summary, 'p95', service.latency_samples ? formatLatency(service.p95_latency_ms) : '—', 'heatmap-p95-value');

    const grid = createElement(documentRef, 'div', 'heat-grid service-indent');
    grid.setAttribute('role', 'grid');
    grid.setAttribute('aria-label', `${service.model} ${rangeLabel(data.range)} health history`);
    const layout = gridLayout(data.range, data.rows, data.columns, service.cells);
    configureGrid(grid, [], layout.rows.length, layout.columnCount);
    const axis = createElement(documentRef, 'div', 'axis service-indent heatmap-axis');
    axis.setAttribute('aria-hidden', 'true');
    for (const label of axisLabels(data.range, data.rows)) {
      axis.append(createElement(documentRef, 'span', '', label));
    }
    panel.append(heading, summary, grid, axis);
    return { panel, grid, layout };
  }

  function appendPanelRows(panelState, service, startRow, endRow) {
    const { grid, layout } = panelState;
    const fragment = documentRef.createDocumentFragment();
    for (let rowIndex = startRow; rowIndex < endRow; rowIndex++) {
      const layoutRow = layout.rows[rowIndex];
      const row = createElement(documentRef, 'div', 'heat-row');
      row.setAttribute('role', 'row');
      row.setAttribute('aria-rowindex', String(rowIndex + 1));
      row.style.gridTemplateColumns = `repeat(${layout.columnCount}, minmax(0, 1fr))`;
      for (const [columnIndex, item] of layoutRow.entries()) {
        const button = createCell(item.cell, service.id, item.sourceIndex);
        button.setAttribute('role', 'gridcell');
        button.setAttribute('aria-colindex', String(columnIndex + 1));
        // 行分批到达时仍只保留一个键盘入口，避免后续再扫描全部格子。
        button.setAttribute('tabindex', rowIndex === 0 && columnIndex === 0 ? '0' : '-1');
        row.append(button);
      }
      fragment.append(row);
    }
    grid.append(fragment);
  }

  function renderCommandModels(services) {
    const commandModels = documentRef.getElementById('cmd-models');
    commandModels.replaceChildren();
    if (!services.length) {
      commandModels.textContent = ' (no services)';
      return;
    }
    for (const service of services) {
      appendText(documentRef, commandModels, ' ');
      const statusClass = {
        healthy: 'ok',
        warning: 'warn',
        failing: 'bad',
        pending: 'warn',
      }[service.status] || 'dim';
      commandModels.append(createElement(documentRef, 'span', statusClass, service.model));
    }
  }

  function updatePanel(panel, service, data) {
    panel.setAttribute('data-service-id', service.id);
    panel.querySelector('.heatmap-model-name').textContent = service.model;
    panel.querySelector('.heatmap-provider').textContent = service.provider ? ` · ${service.provider}` : '';
    const current = panel.querySelector('.heatmap-current');
    current.className = `heatmap-current ${service.status}`;
    current.textContent = ` · ● ${currentStatusLabel(service.status)}`;
    panel.querySelector('.heatmap-uptime-value').textContent = service.samples
      ? `${Number(service.uptime_pct || 0).toFixed(2)}%`
      : '—';
    panel.querySelector('.heatmap-p95-value').textContent = service.latency_samples
      ? formatLatency(service.p95_latency_ms)
      : '—';
    const labels = axisLabels(data.range, data.rows);
    const axisItems = [...panel.querySelector('.heatmap-axis').children];
    if (axisItems.length !== labels.length) return false;
    for (let index = 0; index < labels.length; index++) axisItems[index].textContent = labels[index];
    const cells = [...panel.querySelectorAll('.heat-cell')];
    if (cells.length !== service.cells.length) return false;
    for (const button of cells) {
      const sourceIndex = Number(button.getAttribute('data-cell-index'));
      if (!service.cells[sourceIndex]) return false;
      updateCell(button, service.cells[sourceIndex]);
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
    documentRef.getElementById('active-range').textContent = rangeLabel(data.range);
    documentRef.getElementById('updated').textContent = formatBeijingTime(data.generated_at).slice(11);
    for (const button of documentRef.querySelectorAll?.('[data-range]') || []) {
      const active = button.dataset.range === data.range;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
    }
    const services = data.services || [];
    renderCommandModels(services);
    const layoutKey = `${data.range}|${(data.rows || []).length}|${(data.columns || []).length}`;
    const serviceKey = JSON.stringify(services.map(service => service.id));
    const panels = [...output.querySelectorAll('.heatmap-panel')];
    if (renderedComplete && renderedLayoutKey === layoutKey && renderedServiceKey === serviceKey
      && panels.length === services.length
      && panels.every((panel, index) => updatePanel(panel, services[index], data))) {
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
    let panelState = null;
    let rowIndex = 0;
    let rowsPerFrame = 1;
    // 固定每帧的格子预算，使模型数量增加时 1d/7d 也不会形成长任务。
    const appendNextPanel = () => {
      pendingFrame = null;
      if (version !== renderVersion) return;
      if (!panelState) {
        panelState = createPanel(services[serviceIndex], data);
        output.append(panelState.panel);
        rowIndex = 0;
        rowsPerFrame = Math.max(1, Math.floor(CELLS_PER_FRAME / panelState.layout.columnCount));
      }
      const endRow = Math.min(panelState.layout.rows.length, rowIndex + rowsPerFrame);
      appendPanelRows(panelState, services[serviceIndex], rowIndex, endRow);
      rowIndex = endRow;
      if (rowIndex < panelState.layout.rows.length) {
        pendingFrame = schedule(appendNextPanel);
        return;
      }
      restoreFocusedCell(panelState.panel, focusedCell);
      panelState = null;
      serviceIndex++;
      if (serviceIndex < services.length) pendingFrame = schedule(appendNextPanel);
      else renderedComplete = true;
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
