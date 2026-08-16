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

export function buildTimelineBuckets({
  history = [],
  pauses = [],
  generatedAt,
  historyLength,
  intervalSeconds,
} = {}) {
  const length = Math.min(200, Math.max(1, Number.parseInt(historyLength, 10) || 60));
  const interval = Math.max(1, Number(intervalSeconds) || 60);
  const end = Number(generatedAt) || 0;
  const start = end - length * interval;
  const buckets = Array.from({ length }, (_, index) => ({
    index,
    from: start + index * interval,
    to: start + (index + 1) * interval,
    kind: 'none',
  }));

  for (const pause of pauses || []) {
    const pauseFrom = Number(pause.from);
    const pauseTo = Number(pause.to);
    if (!Number.isFinite(pauseFrom) || !Number.isFinite(pauseTo) || pauseTo <= pauseFrom) continue;
    for (const bucket of buckets) {
      if (pauseFrom >= bucket.to || pauseTo <= bucket.from) continue;
      bucket.kind = 'paused';
      bucket.pause = bucket.pause
        ? { from: Math.min(bucket.pause.from, pauseFrom), to: Math.max(bucket.pause.to, pauseTo) }
        : { from: pauseFrom, to: pauseTo };
    }
  }

  for (const result of history || []) {
    const timestamp = Number(result.ts);
    if (!Number.isFinite(timestamp) || timestamp < start || timestamp > end) continue;
    const index = timestamp === end
      ? length - 1
      : Math.min(length - 1, Math.max(0, Math.floor((timestamp - start) / interval)));
    const current = buckets[index].result;
    if (!current || Number(current.ts) <= timestamp) {
      buckets[index].kind = result.ok ? 'ok' : 'bad';
      buckets[index].result = result;
    }
  }
  return buckets;
}

// 窄屏合并相邻桶，保留完整时间窗口并避免状态条退化为亚像素宽度。
export function compressTimelineBuckets(buckets = [], maxBuckets = buckets.length) {
  const target = Math.max(1, Math.min(buckets.length, Math.floor(Number(maxBuckets) || buckets.length)));
  if (target >= buckets.length) return buckets;
  return Array.from({ length: target }, (_, index) => {
    const fromIndex = Math.floor(index * buckets.length / target);
    const toIndex = Math.max(fromIndex + 1, Math.floor((index + 1) * buckets.length / target));
    const group = buckets.slice(fromIndex, toIndex);
    const results = group.filter(bucket => bucket.result);
    const pauses = group.filter(bucket => bucket.pause);
    const latestResult = candidates => candidates.reduce((latest, candidate) => (
      !latest || Number(latest.result.ts) <= Number(candidate.result.ts) ? candidate : latest
    ), null);
    // 压缩桶保留最严重状态，避免后续成功探测遮住同组内的短暂故障。
    const latestBad = latestResult(results.filter(bucket => bucket.kind === 'bad' || !bucket.result.ok));
    const latest = latestResult(results);
    const compressed = {
      index,
      from: group[0].from,
      to: group.at(-1).to,
      kind: 'none',
    };
    if (latestBad) {
      compressed.kind = 'bad';
      compressed.result = latestBad.result;
    } else if (pauses.length) {
      compressed.kind = 'paused';
      compressed.pause = {
        from: Math.min(...pauses.map(bucket => bucket.pause.from)),
        to: Math.max(...pauses.map(bucket => bucket.pause.to)),
      };
    } else if (latest) {
      compressed.kind = 'ok';
      compressed.result = latest.result;
    }
    return compressed;
  });
}

export function deriveOverallState(services = []) {
  const failing = services.filter(service => service.last && !service.last.ok).length;
  const pending = services.filter(service => !service.last).length;
  const online = services.length - failing - pending;
  let state = 'operational';
  if (!services.length) state = 'empty';
  else if (failing > 0) state = 'degraded';
  else if (pending > 0) state = 'pending';
  return { state, total: services.length, online, failing, pending };
}

export function positionTooltip(targetRect, tooltipRect, viewport, padding = 8, gap = 8) {
  const viewportWidth = Math.max(0, Number(viewport.width) || 0);
  const viewportHeight = Math.max(0, Number(viewport.height) || 0);
  const width = Math.min(Number(tooltipRect.width) || 0, Math.max(0, viewportWidth - padding * 2));
  const height = Math.min(Number(tooltipRect.height) || 0, Math.max(0, viewportHeight - padding * 2));
  const centered = Number(targetRect.left) + Number(targetRect.width) / 2 - width / 2;
  const left = Math.min(
    Math.max(padding, viewportWidth - padding - width),
    Math.max(padding, centered),
  );
  const above = Number(targetRect.top) - gap - height;
  const below = Number(targetRect.top) + Number(targetRect.height) + gap;
  const top = above >= padding
    ? above
    : Math.min(Math.max(padding, viewportHeight - padding - height), Math.max(padding, below));
  return { left, top, placement: above >= padding ? 'above' : 'below' };
}

export function bucketIndexFromPointer(clientX, rect, bucketCount) {
  if (!bucketCount || !Number.isFinite(rect.width) || rect.width <= 0) return 0;
  const ratio = (Number(clientX) - rect.left) / rect.width;
  return Math.min(bucketCount - 1, Math.max(0, Math.floor(ratio * bucketCount)));
}

export function serviceIdentity(service, index) {
  if (service.id) return service.id;
  return `${service.provider || ''}\u0000${service.model || ''}\u0000${index}`;
}

function servicesRenderSignature(services, page, generatedAt) {
  return JSON.stringify({
    display: {
      historyLength: Number(page.history_len) || 60,
      showUptime: Boolean(page.show_uptime),
      showSamples: Boolean(page.show_samples),
      showLatency: Boolean(page.show_latency),
    },
    services: services.map((service, index) => {
      const interval = Math.max(1, Number(service.interval_sec) || 60);
      return {
        identity: serviceIdentity(service, index),
        name: service.name,
        model: service.model,
        provider: service.provider,
        interval,
        window: Math.floor((Number(generatedAt) || 0) / interval),
        uptime: service.uptime_pct,
        last: service.last,
        history: service.history || [],
        pauses: service.pauses || [],
      };
    }),
  });
}

function serviceStatus(service) {
  if (!service.last) return 'pending';
  return service.last.ok ? 'online' : 'failing';
}

function announcementStateKey(services, overallStatus) {
  const serviceStates = services.map((service, index) => [
    serviceIdentity(service, index),
    serviceStatus(service),
  ]).sort(([left], [right]) => left.localeCompare(right));
  return JSON.stringify({ overallStatus, serviceStates });
}

function stableToken(value) {
  let hash = 2166136261;
  for (const character of String(value)) {
    hash ^= character.codePointAt(0);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function bucketKey(bucket) {
  if (bucket.result) return `result:${bucket.result.ts}`;
  if (bucket.pause) return `pause:${bucket.pause.from}:${bucket.pause.to}:${bucket.index}`;
  return `none:${bucket.from}`;
}

function appendText(documentRef, parent, text) {
  parent.append(documentRef.createTextNode(String(text)));
}

function focusWithoutScroll(element) {
  if (!element?.focus) return;
  try { element.focus({ preventScroll: true }); }
  catch { element.focus(); }
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
    status: result.ok ? 'OK' : 'FAIL',
    statusClass: result.ok ? 'ok' : 'bad',
    fields,
  };
}

function timelineDetailModel(bucket) {
  if (!bucket || bucket.kind === 'none') {
    return { status: 'NO SAMPLE', statusClass: 'dim', fields: [] };
  }
  return tooltipModel(bucket);
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
  const announcer = documentRef.getElementById('status-announcer');
  let previousOverallStatus = null;
  let previousServiceStates = new Map();
  const timelineSelections = new Map();
  let tooltipTimer = null;
  let lastSuccessfulAt = null;
  let lastAnnouncementKey = null;
  let lastServicesSignature = null;

  function announce(text, key = text) {
    if (!announcer || key === lastAnnouncementKey) return;
    announcer.textContent = text;
    lastAnnouncementKey = key;
  }

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
    const position = positionTooltip(targetRect, tooltipRect, {
      width: windowRef?.innerWidth || documentRef.documentElement?.clientWidth || 0,
      height: windowRef?.innerHeight || documentRef.documentElement?.clientHeight || 0,
    });
    tip.style.left = `${position.left}px`;
    tip.style.top = `${position.top}px`;
    tip.dataset.placement = position.placement;
  }

  function renderBanner(data, page) {
    const output = documentRef.getElementById('banner-out');
    const services = data.services || [];
    const summaryState = deriveOverallState(services);
    const overallStatus = summaryState.state;
    const statusChanged = previousOverallStatus !== null && previousOverallStatus !== overallStatus;
    const sampledServices = services.filter(service => service.last || (service.history || []).length > 0);
    const averageUptime = sampledServices.length > 0
      ? (sampledServices.reduce((sum, service) => sum + Number(service.uptime_pct || 0), 0) / sampledServices.length).toFixed(2)
      : null;

    const summary = createElement(documentRef, 'div', 'line');
    appendSpan(documentRef, summary, formatTimeShort(data.generated_at), 'dim');
    appendText(documentRef, summary, ' ');
    if (summaryState.state === 'empty') {
      appendSpan(documentRef, summary, 'not configured,', 'warn');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, '0 services', 'cmd');
    } else if (summaryState.state === 'pending') {
      appendSpan(documentRef, summary, 'initializing,', 'warn');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${summaryState.online}/${summaryState.total} checks complete`, 'cmd');
    } else if (summaryState.state === 'operational') {
      appendSpan(documentRef, summary, 'up,', 'ok');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${summaryState.total} services`, 'cmd');
    } else {
      appendSpan(documentRef, summary, 'degraded,', 'bad');
      appendText(documentRef, summary, ' ');
      appendSpan(documentRef, summary, `${summaryState.online}/${summaryState.total} services up`, 'cmd');
      if (summaryState.pending > 0) {
        appendText(documentRef, summary, `, ${summaryState.pending} pending`);
      }
    }
    if (page.show_avg_load && averageUptime !== null) {
      appendText(documentRef, summary, ', ');
      const load = createElement(documentRef, 'span', 'cmd');
      appendText(documentRef, load, 'avg uptime ');
      appendSpan(documentRef, load, `${averageUptime}%`, summaryState.state === 'operational' ? 'ok' : 'warn');
      summary.append(load);
    }

    const detailState = {
      empty: ['warn', '○ no monitoring services configured'],
      pending: ['warn', `◌ initial checks pending · ${summaryState.pending} service${summaryState.pending === 1 ? '' : 's'} awaiting a first result`],
      operational: ['ok', '● all systems operational'],
      degraded: ['bad', `● ${summaryState.failing} service${summaryState.failing === 1 ? '' : 's'} failing · check details below`],
    }[summaryState.state];
    const detailClass = `line ${detailState[0]} bold banner-status${statusChanged ? ' status-change' : ''}`;
    const detailText = detailState[1];
    output.replaceChildren(summary, createElement(documentRef, 'div', detailClass, detailText));
    output.setAttribute('aria-busy', 'false');
    previousOverallStatus = overallStatus;
    return { detailText, overallStatus };
  }

  function renderTimelineDetail(detail, bucket) {
    const model = timelineDetailModel(bucket);
    const status = createElement(documentRef, 'span', `${model.statusClass} bold`, model.status);
    detail.replaceChildren(status);
    for (const [key, value] of model.fields) {
      appendText(documentRef, detail, ` · ${key} `);
      appendSpan(documentRef, detail, value);
    }
  }

  function createTimeline(service, serviceIndex, historyLength, generatedAt) {
    const identity = serviceIdentity(service, serviceIndex);
    const token = stableToken(identity);
    const bars = createElement(documentRef, 'div', 'bars');
    const detail = createElement(documentRef, 'div', 'timeline-detail');
    const detailID = `timeline-detail-${token}`;
    detail.setAttribute('id', detailID);
    bars.setAttribute('role', 'listbox');
    bars.setAttribute('aria-orientation', 'horizontal');
    bars.setAttribute('tabindex', '0');
    bars.setAttribute('data-timeline-service', token);
    bars.setAttribute('aria-label', `History for ${service.model || service.name || service.id || 'service'}`);
    bars.setAttribute('aria-describedby', detailID);

    const rawBuckets = buildTimelineBuckets({
      history: service.history,
      pauses: service.pauses,
      generatedAt,
      historyLength,
      intervalSeconds: service.interval_sec,
    });
    const viewportWidth = windowRef?.innerWidth || documentRef.documentElement?.clientWidth || 0;
    const maxVisibleBuckets = Math.max(48, Math.floor(Math.max(240, viewportWidth - 36) / 4));
    const buckets = compressTimelineBuckets(rawBuckets, maxVisibleBuckets);
    const selectable = buckets.filter(bucket => bucket.kind !== 'none').map(bucket => bucket.index);
    const previousSelection = timelineSelections.get(identity);
    let selectedIndex = buckets.findIndex(bucket => bucketKey(bucket) === previousSelection);
    if (selectedIndex < 0) selectedIndex = selectable.at(-1) ?? -1;
    const barElements = [];

    function nearestSelectable(index) {
      if (!selectable.length) return -1;
      return selectable.reduce((nearest, candidate) => (
        Math.abs(candidate - index) < Math.abs(nearest - index) ? candidate : nearest
      ), selectable[0]);
    }

    function select(index, { show = false } = {}) {
      if (index < 0 || !buckets[index] || buckets[index].kind === 'none') return;
      selectedIndex = index;
      const bucket = buckets[index];
      timelineSelections.set(identity, bucketKey(bucket));
      barElements.forEach((bar, barIndex) => {
        const selected = barIndex === index;
        bar.classList.toggle('selected', selected);
        bar.setAttribute('aria-selected', String(selected));
      });
      bars.setAttribute('aria-activedescendant', barElements[index].getAttribute('id'));
      renderTimelineDetail(detail, bucket);
      if (show) showTooltip(barElements[index], tooltipModel(bucket));
    }

    for (const bucket of buckets) {
      const bar = createElement(documentRef, 'span', `bar ${bucket.kind}`);
      bar.setAttribute('id', `timeline-${token}-${bucket.index}`);
      bar.setAttribute('role', 'option');
      bar.setAttribute('aria-selected', 'false');
      if (bucket.kind === 'none') {
        bar.setAttribute('aria-label', `No sample, ${formatTime(bucket.from)} to ${formatTime(bucket.to)}`);
        bar.setAttribute('aria-disabled', 'true');
      } else {
        bar.setAttribute('aria-label', barAccessibleName(bucket));
        bar.addEventListener('mouseenter', () => showTooltip(bar, tooltipModel(bucket)));
        bar.addEventListener('mouseleave', hideTooltip);
      }
      barElements.push(bar);
      bars.append(bar);
    }

    bars.addEventListener('keydown', event => {
      let nextIndex = selectedIndex;
      if (event.key === 'ArrowLeft') {
        nextIndex = selectable.filter(index => index < selectedIndex).at(-1) ?? selectable[0] ?? -1;
      } else if (event.key === 'ArrowRight') {
        nextIndex = selectable.find(index => index > selectedIndex) ?? selectable.at(-1) ?? -1;
      } else if (event.key === 'Home') {
        nextIndex = selectable[0] ?? -1;
      } else if (event.key === 'End') {
        nextIndex = selectable.at(-1) ?? -1;
      } else if (event.key === 'Escape') {
        hideTooltip();
        return;
      } else {
        return;
      }
      event.preventDefault?.();
      select(nextIndex, { show: true });
    });
    bars.addEventListener('pointerdown', event => {
      const index = nearestSelectable(bucketIndexFromPointer(
        event.clientX,
        bars.getBoundingClientRect(),
        buckets.length,
      ));
      select(index, { show: true });
      bars.focus();
      clearTooltipTimer();
      tooltipTimer = schedule(hideTooltip, 2500);
    });
    bars.addEventListener('blur', hideTooltip);

    if (selectedIndex >= 0) select(selectedIndex);
    else detail.textContent = 'No samples in this window.';
    return { bars, detail };
  }

  function renderServices(services, page, generatedAt) {
    const output = documentRef.getElementById('svc-out');
    const commandModels = documentRef.getElementById('cmd-models');
    const signature = servicesRenderSignature(services, page, generatedAt);
    if (signature === lastServicesSignature) return [];
    const focusedTimeline = documentRef.activeElement?.dataset?.timelineService || null;
    clearTooltipTimer();
    hideTooltip();
    commandModels.replaceChildren();
    if (!services.length) {
      commandModels.textContent = ' (no services)';
    } else {
      for (const service of services) {
        appendText(documentRef, commandModels, ' ');
        const last = service.last;
        const statusClass = last ? (last.ok ? 'ok' : 'bad') : 'warn';
        appendSpan(documentRef, commandModels, service.model, statusClass);
      }
    }

    const historyLength = Number(page.history_len) || 60;
    const fragment = documentRef.createDocumentFragment();
    const nextServiceStates = new Map();
    const announcements = [];
    services.forEach((service, index) => {
      const last = service.last;
      let statusClass = 'warn';
      let statusText = 'pending';
      if (last) {
        statusClass = last.ok ? 'ok' : 'bad';
        statusText = last.ok ? 'online' : 'failing';
      }
      const serviceStatus = last ? (last.ok ? 'ok' : 'bad') : 'pending';
      const key = serviceIdentity(service, index);
      const statusChanged = previousServiceStates.has(key) && previousServiceStates.get(key) !== serviceStatus;
      nextServiceStates.set(key, serviceStatus);
      if (statusChanged) {
        announcements.push(`${service.model || service.name || service.id || 'service'} is now ${statusText}`);
      }

      const heading = createElement(documentRef, 'div', 'line service-heading');
      appendSpan(documentRef, heading, '→', 'mute');
      appendText(documentRef, heading, ' ');
      appendSpan(documentRef, heading, service.model, 'cmd bold');
      appendText(documentRef, heading, ' · ');
      appendSpan(documentRef, heading, `● ${statusText}`, `${statusClass}${statusChanged ? ' status-change' : ''}`);

      const metadata = createElement(documentRef, 'div', 'svc-meta service-indent');
      const uptime = Number(service.uptime_pct || 0);
      const uptimeClass = uptime >= 99 ? 'ok' : (uptime >= 95 ? 'warn' : 'bad');
      const hasSamples = (service.history || []).length > 0;
      if (page.show_uptime) metadata.append(metric(
        documentRef,
        'uptime',
        hasSamples ? `${uptime.toFixed(2)}%` : '—',
        hasSamples ? uptimeClass : 'warn',
      ));
      if (page.show_samples) metadata.append(metric(documentRef, 'samples', `${(service.history || []).length}/${historyLength}`));
      if (page.show_latency && last) metadata.append(metric(documentRef, 'latency', `${last.latency_ms}ms`, last.ok ? 'ok' : 'bad'));

      const barsWrapper = createElement(documentRef, 'div', 'service-indent service-bars');
      const timeline = createTimeline(service, index, historyLength, generatedAt);
      barsWrapper.append(timeline.bars, timeline.detail);
      const axis = createElement(documentRef, 'div', 'axis service-indent');
      axisLabels(historyLength, service.interval_sec || 60).forEach((label, labelIndex) => {
        axis.append(createElement(documentRef, 'span', labelIndex > 0 && labelIndex < 4 ? 'mid-label' : '', label));
      });
      fragment.append(heading, metadata, barsWrapper, axis);
    });
    output.replaceChildren(fragment);
    if (focusedTimeline) {
      focusWithoutScroll(output.querySelector(`[data-timeline-service="${focusedTimeline}"]`));
    }
    previousServiceStates = nextServiceStates;
    lastServicesSignature = signature;
    return announcements;
  }

  function render(data) {
    const page = data.page || {};
    documentRef.title = page.title || 'model-uptime // status';
    documentRef.getElementById('term-subtitle').textContent = page.subtitle || 'model-uptime';
    documentRef.getElementById('probe-comment').textContent = `# ${page.probe_comment || 'model-uptime service monitor · probing every 60s'}`;
    documentRef.getElementById('status-shell')?.classList.remove('stale');
    const services = data.services || [];
    const banner = renderBanner(data, page);
    const serviceAnnouncements = renderServices(services, page, data.generated_at);
    const announcement = serviceAnnouncements.length
      ? `${banner.detailText}. ${serviceAnnouncements.join('; ')}.`
      : banner.detailText;
    announce(announcement, announcementStateKey(services, banner.overallStatus));
    documentRef.getElementById('updated').textContent = formatTimeShort(data.generated_at);
    lastSuccessfulAt = data.generated_at;
  }

  function renderError() {
    const suffix = lastSuccessfulAt
      ? ` · showing data from ${formatTimeShort(lastSuccessfulAt)} · retrying`
      : ' · retrying';
    const line = createElement(documentRef, 'div', 'line bad bold', `● monitor unreachable${suffix}`);
    const banner = documentRef.getElementById('banner-out');
    banner.replaceChildren(line);
    banner.setAttribute('aria-busy', 'false');
    announce(
      lastSuccessfulAt ? 'Monitor unreachable; showing stale data and retrying.' : 'Monitor unreachable; retrying.',
      'monitor-unreachable',
    );
    documentRef.getElementById('status-shell')?.classList.add('stale');
    documentRef.getElementById('updated').textContent = lastSuccessfulAt
      ? `stale · ${formatTimeShort(lastSuccessfulAt)}`
      : 'unavailable';
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
  let inFlight = null;
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
      if (currentEpoch === epoch && sequence === requestSequence) {
        inFlight = null;
      }
      if (active && currentEpoch === epoch && sequence === requestSequence) {
        timer = schedule(() => {
          timer = null;
          if (!active || currentEpoch !== epoch) return;
          return (inFlight = request(currentEpoch));
        }, refreshSeconds * 1000);
      }
    }
  }

  function start() {
    if (active) return inFlight || Promise.resolve();
    cancelTimer();
    active = true;
    epoch++;
    inFlight = request(epoch);
    return inFlight;
  }

  function refresh() {
    if (!active) return start();
    cancelTimer();
    inFlight = request(epoch);
    return inFlight;
  }

  function stop() {
    active = false;
    epoch++;
    requestSequence++;
    inFlight = null;
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
  const poller = createStatusPoller({
    async fetchStatus() {
      const response = await fetchImpl('/api/status', { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return response.json();
    },
    render: renderer.render,
    renderError: renderer.renderError,
    schedule,
    cancel,
  });
  const handleVisibility = () => {
    if (documentRef.hidden) poller.stop();
    else void poller.start();
  };
  const handlePageHide = () => poller.stop();
  const handlePageShow = () => { if (!documentRef.hidden) void poller.start(); };
  documentRef.addEventListener?.('visibilitychange', handleVisibility);
  windowRef?.addEventListener?.('pagehide', handlePageHide);
  windowRef?.addEventListener?.('pageshow', handlePageShow);
  if (!documentRef.hidden) void poller.start();
  return {
    ...poller,
    dispose() {
      poller.stop();
      documentRef.removeEventListener?.('visibilitychange', handleVisibility);
      windowRef?.removeEventListener?.('pagehide', handlePageHide);
      windowRef?.removeEventListener?.('pageshow', handlePageShow);
    },
  };
}

if (typeof document !== 'undefined') startStatusPage();
