import { createTerminalIntro, terminalMotionDisabled } from './terminal-intro.js';

const DEFAULT_REFRESH_SECONDS = 5;
const MIN_REFRESH_SECONDS = 1;
const MAX_REFRESH_SECONDS = 60;

export function pad(value) {
  return String(value).padStart(2, '0');
}

export function formatTime(timestamp) {
  const date = new Date(timestamp * 1000);
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function formatTimeShort(timestamp) {
  const date = new Date(timestamp * 1000);
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function normalizeRefreshSeconds(value, fallback = DEFAULT_REFRESH_SECONDS) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds)) return fallback;
  return Math.min(MAX_REFRESH_SECONDS, Math.max(MIN_REFRESH_SECONDS, seconds));
}

export function axisLabels(historyLength, intervalSeconds) {
  const windowSeconds = historyLength * intervalSeconds;
  const formatAgo = seconds => {
    if (seconds <= 0) return 'now';
    if (seconds % 3600 === 0) return `-${seconds / 3600}h`;
    if (seconds >= 3600) return `-${(seconds / 3600).toFixed(1)}h`;
    return `-${Math.round(seconds / 60)}m`;
  };
  return [0, 1, 2, 3, 4].map(index => formatAgo(Math.round(windowSeconds * (4 - index) / 4)));
}

/** Samples and pause spans share one chronological rendering sequence. */
export function resultStatus(result, warningSeconds = 30) {
  if (!result?.ok) return 'bad';
  return Number(result.latency_ms) > Number(warningSeconds) * 1000 ? 'warning' : 'ok';
}

export function buildBarEvents(history = [], pauses = [], warningSeconds = 30) {
  const events = [];
  for (const result of history || []) {
    events.push({ ts: result.ts, kind: resultStatus(result, warningSeconds), result });
  }
  for (const pause of pauses || []) {
    events.push({ ts: pause.to, kind: 'paused', pause });
  }
  events.sort((left, right) => left.ts - right.ts);
  return events;
}

export function serviceIdentity(service, index) {
  if (service.id) return service.id;
  return `${service.provider || ''}\u0000${service.model || ''}\u0000${index}`;
}

function appendText(documentRef, parent, text) {
  parent.append(documentRef.createTextNode(String(text)));
}

function createElement(documentRef, tagName, className, text) {
  const element = documentRef.createElement(tagName);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = String(text);
  return element;
}

function appendSpan(documentRef, parent, text, className = '') {
  const span = createElement(documentRef, 'span', className, text);
  parent.append(span);
  return span;
}

function metric(documentRef, label, value, className = '') {
  const item = createElement(documentRef, 'span');
  appendText(documentRef, item, `${label} `);
  item.append(createElement(documentRef, 'b', className, value));
  return item;
}

function tooltipModel(event) {
  if (event.kind === 'paused') {
    return {
      status: 'PAUSED',
      statusClass: 'warn',
      fields: [
        ['from', formatTime(event.pause.from)],
        ['to', formatTime(event.pause.to)],
      ],
    };
  }

  const result = event.result;
  const fields = [
    ['at', formatTime(result.ts)],
    ['lat', `${result.latency_ms}ms`],
  ];
  if (result.error) fields.push(['err', String(result.error).slice(0, 80)]);
  return {
    status: event.kind === 'warning' ? 'WARNING' : (result.ok ? 'OK' : 'FAIL'),
    statusClass: event.kind === 'warning' ? 'warn' : (result.ok ? 'ok' : 'bad'),
    fields,
  };
}

function barAccessibleName(event) {
  const model = tooltipModel(event);
  return [model.status, ...model.fields.map(([key, value]) => `${key} ${value}`)].join(', ');
}

export function createStatusRenderer({
  document: documentRef,
  window: windowRef,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
} = {}) {
  if (!documentRef) throw new Error('document is required');

  const tip = documentRef.getElementById('tip');
  let previousOverallStatus = null;
  let previousServiceStates = new Map();
  let tooltipTimer = null;

  function hideTooltip() {
    tip.classList.remove('show');
  }

  function clearTooltipTimer() {
    if (tooltipTimer !== null) cancel(tooltipTimer);
    tooltipTimer = null;
  }

  function showTooltip(target, model) {
    tip.replaceChildren();
    tip.append(createElement(documentRef, 'div', `t-status ${model.statusClass} bold`, model.status));
    for (const [key, value] of model.fields) {
      const row = createElement(documentRef, 'div');
      appendSpan(documentRef, row, key, 't-k');
      appendText(documentRef, row, ` ${value}`);
      tip.append(row);
    }
    tip.classList.add('show');

    const targetRect = target.getBoundingClientRect();
    const tooltipRect = tip.getBoundingClientRect();
    const viewportWidth = windowRef?.innerWidth || documentRef.documentElement?.clientWidth || 0;
    const padding = 8;
    const halfWidth = tooltipRect.width / 2;
    const desiredLeft = targetRect.left + targetRect.width / 2;
    const minLeft = padding + halfWidth;
    const maxLeft = Math.max(minLeft, viewportWidth - padding - halfWidth);
    tip.style.left = `${Math.min(maxLeft, Math.max(minLeft, desiredLeft))}px`;
    tip.style.top = `${targetRect.top - 8}px`;
  }

  function renderBanner(data, page) {
    const output = documentRef.getElementById('banner-out');
    const services = data.services || [];
    const count = services.length;
    const down = services.filter(service => service.last && !service.last.ok).length;
    const warning = services.filter(service => resultStatus(service.last, service.warning_sec) === 'warning').length;
    const overallStatus = down > 0 ? 'bad' : (warning > 0 ? 'warning' : 'ok');
    const statusChanged = previousOverallStatus !== null && previousOverallStatus !== overallStatus;
    const averageUptime = count > 0
      ? (services.reduce((sum, service) => sum + Number(service.uptime_pct || 0), 0) / count).toFixed(2)
      : '100.00';

    const summary = createElement(documentRef, 'div', 'line');
    appendSpan(documentRef, summary, formatTimeShort(data.generated_at), 'dim');
    appendText(documentRef, summary, ' ');
    if (overallStatus === 'ok') {
      appendSpan(documentRef, summary, 'up,', 'ok');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${count} services`, 'cmd');
    } else if (overallStatus === 'warning') {
      appendSpan(documentRef, summary, 'warning,', 'warn');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${warning}/${count} services slow`, 'cmd');
    } else {
      appendSpan(documentRef, summary, 'degraded,', 'bad');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${count - down}/${count} services up`, 'cmd');
    }
    if (page.show_avg_load) {
      appendText(documentRef, summary, ', ');
      const load = createElement(documentRef, 'span', 'cmd');
      appendText(documentRef, load, 'avg load ');
      appendSpan(documentRef, load, `${averageUptime}%`, overallStatus === 'ok' ? 'ok' : 'warn');
      summary.append(load);
    }

    const detailStatusClass = overallStatus === 'warning' ? 'warn' : overallStatus;
    const detailClass = `line ${detailStatusClass} bold banner-status${statusChanged ? ' status-change' : ''}`;
    let detailText = '● all systems operational';
    if (overallStatus === 'warning') {
      detailText = `● ${warning} service${warning === 1 ? '' : 's'} responding slowly`;
    } else if (overallStatus === 'bad') {
      detailText = `● ${down} service${down === 1 ? '' : 's'} failing — check logs below`;
    }
    output.replaceChildren(summary, createElement(documentRef, 'div', detailClass, detailText));
    previousOverallStatus = overallStatus;
  }

  function createHistoryBar(event) {
    const bar = createElement(documentRef, 'button', `bar ${event.kind}`);
    bar.type = 'button';
    bar.setAttribute('aria-label', barAccessibleName(event));
    bar.setAttribute('aria-describedby', 'tip');
    const show = () => showTooltip(bar, tooltipModel(event));
    bar.addEventListener('mouseenter', show);
    bar.addEventListener('mouseleave', hideTooltip);
    bar.addEventListener('focus', show);
    bar.addEventListener('blur', hideTooltip);
    bar.addEventListener('keydown', eventObject => {
      if (eventObject.key === 'Escape') {
        hideTooltip();
        bar.blur();
      }
    });
    bar.addEventListener('click', () => {
      show();
      clearTooltipTimer();
      tooltipTimer = schedule(hideTooltip, 2500);
    });
    return bar;
  }

  function createBars(service, historyLength) {
    const bars = createElement(documentRef, 'div', 'bars');
    const recent = buildBarEvents(service.history, service.pauses, service.warning_sec).slice(-historyLength);
    const padCount = historyLength - recent.length;
    for (let index = 0; index < padCount; index++) {
      bars.append(createElement(documentRef, 'span', 'bar none'));
    }
    for (const event of recent) bars.append(createHistoryBar(event));
    return bars;
  }

  function renderServices(services, page) {
    const output = documentRef.getElementById('svc-out');
    const commandModels = documentRef.getElementById('cmd-models');
    clearTooltipTimer();
    hideTooltip();
    commandModels.replaceChildren();
    if (!services.length) {
      commandModels.textContent = ' (no services)';
    } else {
      for (const service of services) {
        appendText(documentRef, commandModels, ' ');
        const last = service.last;
        const status = last ? resultStatus(last, service.warning_sec) : 'pending';
        const statusClass = status === 'warning' || status === 'pending' ? 'warn' : status;
        appendSpan(documentRef, commandModels, service.model, statusClass);
      }
    }

    const historyLength = Number(page.history_len) || 60;
    const fragment = documentRef.createDocumentFragment();
    const nextServiceStates = new Map();
    services.forEach((service, index) => {
      const last = service.last;
      let statusClass = 'warn';
      let statusText = 'pending';
      if (last) {
        const status = resultStatus(last, service.warning_sec);
        statusClass = status === 'warning' ? 'warn' : status;
        statusText = status === 'warning' ? 'slow' : (status === 'ok' ? 'online' : 'failing');
      }
      const serviceStatus = last ? resultStatus(last, service.warning_sec) : 'pending';
      const key = serviceIdentity(service, index);
      const statusChanged = previousServiceStates.has(key) && previousServiceStates.get(key) !== serviceStatus;
      nextServiceStates.set(key, serviceStatus);

      const heading = createElement(documentRef, 'div', 'line service-heading');
      appendSpan(documentRef, heading, '→', 'mute');
      appendText(documentRef, heading, ' ');
      appendSpan(documentRef, heading, service.model, 'cmd bold');
      appendText(documentRef, heading, ' · ');
      appendSpan(documentRef, heading, `● ${statusText}`, `${statusClass}${statusChanged ? ' status-change' : ''}`);

      const metadata = createElement(documentRef, 'div', 'svc-meta service-indent');
      const uptime = Number(service.uptime_pct || 0);
      const uptimeClass = uptime >= 99 ? 'ok' : (uptime >= 95 ? 'warn' : 'bad');
      if (page.show_uptime) metadata.append(metric(documentRef, 'uptime', `${uptime.toFixed(2)}%`, uptimeClass));
      if (page.show_samples) metadata.append(metric(documentRef, 'samples', `${(service.history || []).length}/${historyLength}`));
      if (page.show_latency && last) {
        const latencyStatus = resultStatus(last, service.warning_sec);
        metadata.append(metric(documentRef, 'latency', `${last.latency_ms}ms`, latencyStatus === 'warning' ? 'warn' : latencyStatus));
      }

      const barsWrapper = createElement(documentRef, 'div', 'service-indent service-bars');
      barsWrapper.append(createBars(service, historyLength));
      const axis = createElement(documentRef, 'div', 'axis service-indent');
      axisLabels(historyLength, service.interval_sec || 60).forEach((label, labelIndex) => {
        axis.append(createElement(documentRef, 'span', labelIndex > 0 && labelIndex < 4 ? 'mid-label' : '', label));
      });
      fragment.append(heading, metadata, barsWrapper, axis);
    });
    output.replaceChildren(fragment);
    previousServiceStates = nextServiceStates;
  }

  function render(data) {
    const page = data.page || {};
    documentRef.title = page.title || 'model-uptime // status';
    documentRef.getElementById('term-subtitle').textContent = page.subtitle || 'model-uptime';
    documentRef.getElementById('probe-comment').textContent = `# ${page.probe_comment || 'model-uptime · service health and performance'}`;
    renderBanner(data, page);
    renderServices(data.services || [], page);
    documentRef.getElementById('updated').textContent = formatTimeShort(data.generated_at);
  }

  function renderError() {
    const line = createElement(documentRef, 'div', 'line bad bold', '● monitor unreachable');
    documentRef.getElementById('banner-out').replaceChildren(line);
  }

  return { render, renderError, hideTooltip };
}

export function createStatusPoller({
  fetchStatus,
  render,
  renderError,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  defaultRefreshSeconds = DEFAULT_REFRESH_SECONDS,
} = {}) {
  let active = false;
  let epoch = 0;
  let requestSequence = 0;
  let timer = null;
  let refreshSeconds = normalizeRefreshSeconds(defaultRefreshSeconds);

  function cancelTimer() {
    if (timer !== null) cancel(timer);
    timer = null;
  }

  async function request(currentEpoch) {
    const sequence = ++requestSequence;
    try {
      const data = await fetchStatus();
      if (!active || currentEpoch !== epoch || sequence !== requestSequence) return;
      render(data);
      refreshSeconds = normalizeRefreshSeconds(data.page?.refresh_sec, refreshSeconds);
    } catch (error) {
      if (!active || currentEpoch !== epoch || sequence !== requestSequence) return;
      renderError(error);
    } finally {
      if (active && currentEpoch === epoch && sequence === requestSequence) {
        timer = schedule(() => { void request(currentEpoch); }, refreshSeconds * 1000);
      }
    }
  }

  function start() {
    cancelTimer();
    active = true;
    epoch++;
    return request(epoch);
  }

  function refresh() {
    if (!active) return start();
    cancelTimer();
    return request(epoch);
  }

  function stop() {
    active = false;
    epoch++;
    requestSequence++;
    cancelTimer();
  }

  return { start, refresh, stop };
}

export function startStatusPage({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  fetch: fetchImpl = globalThis.fetch,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  now = () => Date.now(),
} = {}) {
  documentRef.getElementById('login-time').textContent = formatTime(Math.floor(now() / 1000));
  const renderer = createStatusRenderer({ document: documentRef, window: windowRef, schedule, cancel });
  const intro = createTerminalIntro({
    root: documentRef.getElementById('terminal'),
    schedule,
    disabled: terminalMotionDisabled({ document: documentRef, window: windowRef }),
    stages: [
      {
        command: documentRef.getElementById('command-uptime'),
        duration: 192,
        pause: 80,
        reveal: [documentRef.getElementById('banner-out')],
      },
      {
        command: documentRef.getElementById('command-monitor'),
        duration: 480,
        reveal: [
          documentRef.getElementById('cmd-models'),
          documentRef.getElementById('svc-out'),
        ],
      },
    ],
  });
  const poller = createStatusPoller({
    async fetchStatus() {
      const response = await fetchImpl('/api/status', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return response.json();
    },
    render(data) {
      renderer.render(data);
      intro.setDataReady();
    },
    renderError(error) {
      renderer.renderError(error);
      intro.setDataReady();
    },
    schedule,
    cancel,
  });
  intro.start();
  void poller.start();
  return poller;
}

if (typeof document !== 'undefined') startStatusPage();
