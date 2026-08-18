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

function appendMetric(documentRef, parent, label, value) {
  const metric = createElement(documentRef, 'span');
  appendText(documentRef, metric, `${label} `);
  metric.append(createElement(documentRef, 'b', '', value));
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

export function createHeatmapRenderer({ document: documentRef, window: windowRef } = {}) {
  if (!documentRef) throw new Error('document is required');
  const output = documentRef.getElementById('heatmap-out');
  const tip = documentRef.getElementById('tip');

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

  function createCell(cell, serviceID, cellIndex) {
    const intensity = Math.min(5, Math.max(0, Number(cell.intensity) || 0));
    const intensityClass = intensity > 0 ? ` intensity-${intensity}` : '';
    const button = createElement(documentRef, 'button', `heat-cell ${cell.status}${intensityClass}`);
    button.type = 'button';
    button.setAttribute('aria-label', accessibleCellName(cell));
    button.setAttribute('aria-describedby', 'tip');
    button.setAttribute('data-service-id', serviceID);
    button.setAttribute('data-cell-index', String(cellIndex));
    const show = () => showTooltip(button, cellTooltipModel(cell));
    button.addEventListener('mouseenter', show);
    button.addEventListener('mouseleave', hideTooltip);
    button.addEventListener('focus', show);
    button.addEventListener('blur', hideTooltip);
    button.addEventListener('click', show);
    return button;
  }

  function enableGridKeyboardNavigation(grid, cells, rowCount, columnCount) {
    if (!cells.length) return;
    const focusCell = index => {
      const nextIndex = Math.min(cells.length - 1, Math.max(0, index));
      for (let cellIndex = 0; cellIndex < cells.length; cellIndex++) {
        cells[cellIndex].setAttribute('tabindex', cellIndex === nextIndex ? '0' : '-1');
      }
      cells[nextIndex].focus();
    };
    for (let index = 0; index < cells.length; index++) {
      const button = cells[index];
      button.setAttribute('tabindex', index === 0 ? '0' : '-1');
      button.addEventListener('focus', () => {
        for (const cell of cells) cell.setAttribute('tabindex', cell === button ? '0' : '-1');
      });
      button.addEventListener('keydown', event => {
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
        focusCell(nextIndex);
      });
    }
    grid.setAttribute('aria-rowcount', String(rowCount));
    grid.setAttribute('aria-colcount', String(columnCount + 1));
  }

  function createPanel(service, data) {
    const panel = createElement(documentRef, 'section', 'heatmap-panel');
    const heading = createElement(documentRef, 'div', 'heatmap-panel-heading');
    const identity = createElement(documentRef, 'div', 'heatmap-model bold');
    identity.append(createElement(documentRef, 'span', '', service.model));
    if (service.provider) {
      appendText(documentRef, identity, ' ');
      identity.append(createElement(documentRef, 'span', 'heatmap-provider', `· ${service.provider}`));
    }
    heading.append(identity, createElement(documentRef, 'span', `heatmap-current ${service.status}`, `● ${service.status}`));

    const summary = createElement(documentRef, 'div', 'heatmap-summary');
    appendMetric(documentRef, summary, 'uptime', service.samples ? `${Number(service.uptime_pct || 0).toFixed(2)}%` : '—');
    appendMetric(documentRef, summary, 'p95', service.latency_samples ? formatLatency(service.p95_latency_ms) : '—');

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
    enableGridKeyboardNavigation(grid, gridCells, (data.rows || []).length, columnCount);
    panel.append(heading, summary, axis, grid);
    return panel;
  }

  function render(data) {
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
    const fragment = documentRef.createDocumentFragment();
    if (!(data.services || []).length) {
      fragment.append(createElement(documentRef, 'div', 'heatmap-empty', 'no enabled services'));
    } else {
      for (const service of data.services) fragment.append(createPanel(service, data));
    }
    output.replaceChildren(fragment);
    if (focusedCell) {
      const replacement = [...output.querySelectorAll('.heat-cell')].find(cell => (
        cell.getAttribute('data-service-id') === focusedCell.serviceID
        && cell.getAttribute('data-cell-index') === focusedCell.cellIndex
      ));
      replacement?.focus();
    }
  }

  function renderError() {
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
