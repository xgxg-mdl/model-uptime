import { createTerminalIntro, terminalMotionDisabled } from './terminal-intro.js';
import { createWindowManager } from './window-manager.js';

const DEFAULT_REFRESH_SECONDS = 5;
const MIN_REFRESH_SECONDS = 1;
const MAX_REFRESH_SECONDS = 60;
const DEFAULT_PAGE = {
  title: 'model-uptime // status',
  subtitle: 'model-uptime',
  probe_comment: 'model-uptime · service health and performance',
  history_len: 60,
  refresh_sec: DEFAULT_REFRESH_SECONDS,
  enable_command_animation: true,
  show_uptime: true,
  show_samples: true,
  show_latency: true,
  show_avg_load: true,
};

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

export function axisLabels(slotCount, intervalSeconds) {
  const windowSeconds = slotCount * intervalSeconds;
  const formatAgo = seconds => {
    if (seconds <= 0) return 'now';
    if (seconds % 3600 === 0) return `-${seconds / 3600}h`;
    if (seconds >= 3600) return `-${(seconds / 3600).toFixed(1)}h`;
    return `-${Math.round(seconds / 60)}m`;
  };
  return [0, 1, 2, 3, 4].map(index => formatAgo(Math.round((windowSeconds * (4 - index)) / 4)));
}

export function resultStatus(result, warningSeconds = 30) {
  if (!result?.ok) return 'bad';
  return Number(result.latency_ms) > Number(warningSeconds) * 1000 ? 'warning' : 'ok';
}

const TIMELINE_PRESENTATION = new Map([
  ['healthy', { status: 'healthy', barClass: 'ok', label: 'OK', tooltipClass: 'ok', observed: true }],
  ['slow', { status: 'slow', barClass: 'warning', label: 'WARNING', tooltipClass: 'warn', observed: true }],
  ['failing', { status: 'failing', barClass: 'bad', label: 'FAIL', tooltipClass: 'bad', observed: true }],
  ['probing', { status: 'probing', barClass: 'probing', label: 'PROBING', tooltipClass: 'dim', observed: true }],
  ['paused', { status: 'paused', barClass: 'paused', label: 'PAUSED', tooltipClass: 'warn', observed: false }],
  [
    'unobserved',
    { status: 'unobserved', barClass: 'unobserved', label: 'NO DATA', tooltipClass: 'dim', observed: false },
  ],
  [
    'not-started',
    {
      status: 'not-started',
      barClass: 'not-started',
      label: 'NOT STARTED',
      tooltipClass: 'dim',
      observed: false,
    },
  ],
]);

function timelinePresentation(status) {
  return TIMELINE_PRESENTATION.get(status) || TIMELINE_PRESENTATION.get('unobserved');
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

function tooltipModel(slot) {
  const presentation = timelinePresentation(slot.status);
  if (presentation.status === 'probing') {
    const probeStartedAt = Number(slot.probe_started_at) || Number(slot.start_ts);
    return {
      status: presentation.label,
      statusClass: presentation.tooltipClass,
      fields: [
        ['since', formatTime(probeStartedAt)],
        ['slot', `${formatTime(slot.start_ts)} — ${formatTime(slot.end_ts)}`],
      ],
    };
  }
  if (presentation.status === 'paused') {
    return {
      status: presentation.label,
      statusClass: presentation.tooltipClass,
      fields: [
        ['from', formatTime(slot.start_ts)],
        ['to', formatTime(slot.end_ts)],
      ],
    };
  }
  if (presentation.status === 'unobserved' || presentation.status === 'not-started' || !slot.result) {
    return {
      status: presentation.label,
      statusClass: presentation.tooltipClass,
      fields: [
        ['from', formatTime(slot.start_ts)],
        ['to', formatTime(slot.end_ts)],
      ],
    };
  }

  const result = slot.result;
  const fields = [
    ['at', formatTime(result.ts)],
    ['lat', `${result.latency_ms}ms`],
  ];
  if (result.error) fields.push(['err', String(result.error).slice(0, 80)]);
  return {
    status: presentation.label,
    statusClass: presentation.tooltipClass,
    fields,
  };
}

function barAccessibleName(slot) {
  const model = tooltipModel(slot);
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
  let pageConfig = { ...DEFAULT_PAGE };
  let latestData = null;

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
    const overallStatus = down > 0 ? 'bad' : warning > 0 ? 'warning' : 'ok';
    const statusChanged = previousOverallStatus !== null && previousOverallStatus !== overallStatus;
    const averageUptime =
      count > 0
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

  function createTimelineBar(slot) {
    const bar = createElement(documentRef, 'button', `bar ${timelinePresentation(slot.status).barClass}`);
    bar.type = 'button';
    bar.setAttribute('aria-label', barAccessibleName(slot));
    bar.setAttribute('aria-describedby', 'tip');
    const show = () => showTooltip(bar, tooltipModel(slot));
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

  function createBars(timeline) {
    const bars = createElement(documentRef, 'div', 'bars');
    for (const slot of timeline) bars.append(createTimelineBar(slot));
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
        appendSpan(documentRef, commandModels, service.model, 'str');
      }
    }

    const fragment = documentRef.createDocumentFragment();
    const nextServiceStates = new Map();
    services.forEach((service, index) => {
      const timeline = Array.isArray(service.timeline) ? service.timeline : [];
      const last = service.last;
      let statusClass = 'warn';
      let statusText = 'pending';
      if (last) {
        const status = resultStatus(last, service.warning_sec);
        statusClass = status === 'warning' ? 'warn' : status;
        statusText = status === 'warning' ? 'slow' : status === 'ok' ? 'online' : 'failing';
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
      const uptimeClass = uptime >= 99 ? 'ok' : uptime >= 95 ? 'warn' : 'bad';
      if (page.show_uptime) metadata.append(metric(documentRef, 'uptime', `${uptime.toFixed(2)}%`, uptimeClass));
      if (page.show_samples) {
        const observedSlots = timeline.filter(slot => timelinePresentation(slot.status).observed).length;
        metadata.append(metric(documentRef, 'coverage', `${observedSlots}/${timeline.length}`));
      }
      if (page.show_latency && last) {
        const latencyStatus = resultStatus(last, service.warning_sec);
        metadata.append(
          metric(documentRef, 'latency', `${last.latency_ms}ms`, latencyStatus === 'warning' ? 'warn' : latencyStatus),
        );
      }

      const barsWrapper = createElement(documentRef, 'div', 'service-indent service-bars');
      barsWrapper.append(createBars(timeline));
      const axis = createElement(documentRef, 'div', 'axis service-indent');
      axisLabels(timeline.length, service.interval_sec || 60).forEach((label, labelIndex) => {
        axis.append(createElement(documentRef, 'span', labelIndex > 0 && labelIndex < 4 ? 'mid-label' : '', label));
      });
      fragment.append(heading, metadata, barsWrapper, axis);
    });
    output.replaceChildren(fragment);
    previousServiceStates = nextServiceStates;
  }

  function renderData(data) {
    renderBanner(data, pageConfig);
    renderServices(data.services || [], pageConfig);
    documentRef.getElementById('updated').textContent = formatTimeShort(data.generated_at);
  }

  function render(data) {
    latestData = data;
    renderData(data);
  }

  function renderPage(page = {}) {
    pageConfig = { ...DEFAULT_PAGE, ...page };
    documentRef.title = pageConfig.title;
    documentRef.getElementById('term-subtitle').textContent = pageConfig.subtitle;
    documentRef.getElementById('probe-comment').textContent =
      `# ${pageConfig.probe_comment || DEFAULT_PAGE.probe_comment}`;
    if (latestData) renderData(latestData);
  }

  function renderError() {
    const line = createElement(documentRef, 'div', 'line bad bold', '● monitor unreachable');
    documentRef.getElementById('banner-out').replaceChildren(line);
  }

  return { render, renderError, renderPage, hideTooltip };
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
    } catch (error) {
      if (!active || currentEpoch !== epoch || sequence !== requestSequence) return;
      renderError(error);
    } finally {
      if (active && currentEpoch === epoch && sequence === requestSequence) {
        timer = schedule(() => {
          void request(currentEpoch);
        }, refreshSeconds * 1000);
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

  function setRefreshSeconds(value) {
    refreshSeconds = normalizeRefreshSeconds(value, refreshSeconds);
    if (!active || timer === null) return;
    cancelTimer();
    timer = schedule(() => {
      void request(epoch);
    }, refreshSeconds * 1000);
  }

  function stop() {
    active = false;
    epoch++;
    requestSequence++;
    cancelTimer();
  }

  return { start, refresh, setRefreshSeconds, stop };
}

export function startStatusPage({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  fetch: fetchImpl = globalThis.fetch,
  schedule = globalThis.setTimeout,
  cancel = globalThis.clearTimeout,
  now = () => Date.now(),
} = {}) {
  createWindowManager({ document: documentRef, window: windowRef, schedule });
  documentRef.getElementById('login-time').textContent = formatTime(Math.floor(now() / 1000));
  const renderer = createStatusRenderer({
    document: documentRef,
    window: windowRef,
    schedule,
    cancel,
  });
  let commandAnimationEnabled = true;
  const intro = createTerminalIntro({
    root: documentRef.getElementById('terminal'),
    schedule,
    disabled: () =>
      !commandAnimationEnabled ||
      terminalMotionDisabled({
        document: documentRef,
        window: windowRef,
      }),
    stages: [
      {
        command: documentRef.getElementById('command-uptime'),
        pause: 80,
        reveal: [documentRef.getElementById('banner-out')],
      },
      {
        command: documentRef.getElementById('command-monitor'),
        reveal: [documentRef.getElementById('svc-out')],
      },
    ],
  });
  let poller;
  async function loadPage() {
    try {
      const response = await fetchImpl('/api/page', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const page = await response.json();
      commandAnimationEnabled = page.enable_command_animation !== false;
      renderer.renderPage(page);
      poller.setRefreshSeconds(page.refresh_sec);
    } catch {
      renderer.renderPage();
    } finally {
      intro.start();
    }
  }
  poller = createStatusPoller({
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
  void loadPage();
  void poller.start();
  return poller;
}

if (typeof document !== 'undefined') startStatusPage();
