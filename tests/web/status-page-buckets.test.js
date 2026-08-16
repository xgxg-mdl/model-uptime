import assert from 'node:assert/strict';
import test from 'node:test';

import {
  bucketIndexFromPointer,
  buildTimelineBuckets,
  compressTimelineBuckets,
  createStatusPoller,
  createStatusRenderer,
  deriveOverallState,
  positionTooltip,
} from '../../internal/httpserver/web/assets/scripts/status-page.js';
import { createStatusDocument, findAll } from './helpers/fake-dom.js';

const GENERATED_AT = 1_700_000_000;

function statusData(serviceOrServices, page = {}) {
  const services = Array.isArray(serviceOrServices) ? serviceOrServices : [serviceOrServices];
  return {
    generated_at: GENERATED_AT,
    all_ok: services.every(service => service.last?.ok !== false),
    services,
    page: {
      title: 'Status',
      subtitle: 'model-uptime',
      probe_comment: 'checking services',
      history_len: 5,
      refresh_sec: 5,
      show_uptime: true,
      show_samples: true,
      show_latency: true,
      show_avg_load: true,
      ...page,
    },
  };
}

function service(overrides = {}) {
  const result = { ts: GENERATED_AT - 30, ok: true, latency_ms: 12 };
  return {
    id: 'service-1',
    name: 'Primary Model',
    provider: 'OpenAI',
    model: 'gpt-5',
    interval_sec: 60,
    uptime_pct: 99.9,
    history: [result],
    pauses: [],
    last: result,
    ...overrides,
  };
}

function render(data) {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data);
  return { document, renderer };
}

test('固定时间桶按暂停持续时间铺满且不会挤掉真实样本', () => {
  const buckets = buildTimelineBuckets({
    generatedAt: 3_600,
    historyLength: 60,
    intervalSeconds: 60,
    pauses: [{ from: 600, to: 2_400 }],
    history: [
      { ts: 1_230, ok: true, latency_ms: 10 },
      { ts: 2_390, ok: false, latency_ms: 20 },
    ],
  });

  assert.equal(buckets.length, 60);
  assert.equal(buckets.filter(bucket => bucket.kind === 'paused').length, 28);
  assert.equal(buckets.filter(bucket => bucket.kind === 'ok').length, 1);
  assert.equal(buckets.filter(bucket => bucket.kind === 'bad').length, 1);
  assert.equal(buckets[20].result.ts, 1_230);
  assert.equal(buckets[39].result.ts, 2_390);
});

test('固定时间桶丢弃窗口外样本，并在同一桶保留最新结果', () => {
  const buckets = buildTimelineBuckets({
    generatedAt: 600,
    historyLength: 5,
    intervalSeconds: 60,
    history: [
      { ts: 299, ok: false, latency_ms: 1 },
      { ts: 315, ok: false, latency_ms: 2 },
      { ts: 350, ok: true, latency_ms: 3 },
      { ts: 590, ok: false, latency_ms: 4 },
    ],
    pauses: [{ from: 0, to: 330 }],
  });

  assert.deepEqual(buckets.map(bucket => bucket.kind), ['ok', 'none', 'none', 'none', 'bad']);
  assert.equal(buckets[0].result.ts, 350);
  assert.equal(buckets[4].result.ts, 590);
});

test('窄屏时间轴合并桶但保留完整时间窗口', () => {
  const raw = buildTimelineBuckets({
    generatedAt: 1_200,
    historyLength: 12,
    intervalSeconds: 60,
    history: [{ ts: 1_170, ok: false, latency_ms: 20 }],
    pauses: [{ from: 300, to: 420 }],
  });
  const compressed = compressTimelineBuckets(raw, 4);
  assert.equal(compressed.length, 4);
  assert.equal(compressed[0].from, raw[0].from);
  assert.equal(compressed.at(-1).to, raw.at(-1).to);
  assert.equal(compressed.at(-1).kind, 'bad');

  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 320, innerHeight: 640 } });
  renderer.render(statusData(service({ history: [
    { ts: GENERATED_AT - 30, ok: true, latency_ms: 10 },
  ] }), { history_len: 200 }));
  const bars = findAll(document.getElementById('svc-out'), element => element.classList.contains('bars'))[0];
  assert.ok(bars.children.length <= 71, 'narrow timeline should keep bars readable');
});

test('窄屏桶聚合按 bad、paused、ok 优先级并保留对应详情', () => {
  const bucket = (index, details = {}) => ({
    index,
    from: index * 10,
    to: (index + 1) * 10,
    kind: 'none',
    ...details,
  });
  const badEarly = { ts: 10, ok: false, latency_ms: 11, error: 'early failure' };
  const badLatest = { ts: 20, ok: false, latency_ms: 22, error: 'latest failure' };
  const okLatest = { ts: 30, ok: true, latency_ms: 33 };
  const compressed = compressTimelineBuckets([
    bucket(0, { kind: 'bad', result: badEarly, pause: { from: 1, to: 9 } }),
    bucket(1, { kind: 'bad', result: badLatest }),
    bucket(2, { kind: 'ok', result: okLatest }),
    bucket(3, { kind: 'paused', pause: { from: 31, to: 34 } }),
    bucket(4, { kind: 'paused', pause: { from: 44, to: 48 } }),
    bucket(5, { kind: 'ok', result: { ts: 50, ok: true, latency_ms: 55 } }),
    bucket(6, { kind: 'ok', result: { ts: 60, ok: true, latency_ms: 66 } }),
    bucket(7),
    bucket(8, { kind: 'ok', result: { ts: 80, ok: true, latency_ms: 88 } }),
  ], 3);

  assert.deepEqual(compressed.map(item => item.kind), ['bad', 'paused', 'ok']);
  assert.equal(compressed[0].result, badLatest);
  assert.equal(compressed[0].pause, undefined);
  assert.deepEqual(compressed[1].pause, { from: 31, to: 48 });
  assert.equal(compressed[1].result, undefined);
  assert.equal(compressed[2].result.ts, 80);
});

test('状态页把服务字段和错误详情作为纯文本渲染', () => {
  const injectedName = '<img src=x onerror="globalThis.pwned=true">';
  const injectedError = '<script>globalThis.pwned=true</script>';
  const failedAt = GENERATED_AT - 30;
  const failing = service({
    name: injectedName,
    model: injectedName,
    history: [{ ts: failedAt, ok: false, latency_ms: 18, error: injectedError }],
    last: { ts: failedAt, ok: false, latency_ms: 18, error: injectedError },
  });
  const { document } = render(statusData(failing));
  const output = document.getElementById('svc-out');

  assert.match(output.textContent, /<img src=x onerror=/);
  assert.equal(findAll(output, element => ['IMG', 'SCRIPT'].includes(element.tagName)).length, 0);

  const badBar = findAll(output, element => element.classList.contains('bar') && element.classList.contains('bad'))[0];
  assert.ok(badBar, '失败样本应渲染为时间轴选项');
  badBar.dispatchEvent({ type: 'mouseenter' });

  const tooltip = document.getElementById('tip');
  assert.ok(tooltip.classList.contains('show'));
  assert.match(tooltip.textContent, /<script>globalThis\.pwned=true<\/script>/);
  assert.equal(findAll(tooltip, element => element.tagName === 'SCRIPT').length, 0);
  assert.match(badBar.getAttribute('aria-label'), /FAIL/);

  const timeline = findAll(output, element => element.getAttribute('role') === 'listbox')[0];
  timeline.focus();
  timeline.dispatchEvent({ type: 'keydown', key: 'Escape' });
  assert.equal(tooltip.classList.contains('show'), false);
  assert.equal(document.activeElement, timeline);
});

test('总体状态区分空配置、待首检、正常和故障', () => {
  const pending = service({ id: 'pending', history: [], last: null });
  const online = service({ id: 'online' });
  const failingResult = { ts: GENERATED_AT - 20, ok: false, latency_ms: 30 };
  const failing = service({ id: 'failing', history: [failingResult], last: failingResult });

  assert.equal(deriveOverallState([]).state, 'empty');
  assert.deepEqual(deriveOverallState([pending]), {
    state: 'pending', total: 1, online: 0, failing: 0, pending: 1,
  });
  assert.equal(deriveOverallState([online]).state, 'operational');
  assert.deepEqual(deriveOverallState([pending, online, failing]), {
    state: 'degraded', total: 3, online: 1, failing: 1, pending: 1,
  });

  const emptyBanner = render(statusData([])).document.getElementById('banner-out').textContent;
  assert.match(emptyBanner, /no monitoring services configured/);
  assert.doesNotMatch(emptyBanner, /all systems operational|100\.00%/);

  const pendingBanner = render(statusData([pending, online])).document.getElementById('banner-out').textContent;
  assert.match(pendingBanner, /initial checks pending/);
  assert.doesNotMatch(pendingBanner, /all systems operational/);

  const degradedBanner = render(statusData([pending, failing])).document.getElementById('banner-out').textContent;
  assert.match(degradedBanner, /1 service failing/);
  assert.match(degradedBanner, /1 pending/);
});

test('每个服务只有一个时间轴 Tab 停点，方向键选择并在轮询后恢复焦点', () => {
  const early = { ts: GENERATED_AT - 250, ok: true, latency_ms: 10 };
  const late = { ts: GENERATED_AT - 30, ok: false, latency_ms: 20 };
  const data = statusData(service({ history: [early, late], last: late }));
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 390, innerHeight: 700 } });
  renderer.render(data);

  const output = document.getElementById('svc-out');
  let timelines = findAll(output, element => element.getAttribute('role') === 'listbox');
  assert.equal(timelines.length, 1);
  assert.equal(findAll(output, element => element.getAttribute('tabindex') === '0').length, 1);
  assert.equal(findAll(output, element => element.tagName === 'BUTTON').length, 0);

  const focusedTimeline = timelines[0];
  let focusEvents = 0;
  focusedTimeline.addEventListener('focus', () => { focusEvents++; });
  focusedTimeline.focus();
  const latestSelection = timelines[0].getAttribute('aria-activedescendant');
  timelines[0].dispatchEvent({ type: 'keydown', key: 'ArrowLeft', preventDefault() {} });
  const earlierSelection = timelines[0].getAttribute('aria-activedescendant');
  assert.notEqual(earlierSelection, latestSelection);

  renderer.render({ ...data, generated_at: GENERATED_AT + 5 });
  timelines = findAll(output, element => element.getAttribute('role') === 'listbox');
  assert.equal(timelines[0], focusedTimeline);
  assert.equal(document.activeElement, timelines[0]);
  assert.equal(focusEvents, 1);
  assert.equal(timelines[0].getAttribute('aria-activedescendant'), earlierSelection);

  timelines[0].dispatchEvent({ type: 'keydown', key: 'End', preventDefault() {} });
  assert.equal(timelines[0].getAttribute('aria-activedescendant'), latestSelection);
});

test('窄屏指针映射和 tooltip 定位均夹在视口内', () => {
  const rect = { left: 10, top: 4, width: 300, height: 30 };
  assert.equal(bucketIndexFromPointer(-100, rect, 200), 0);
  assert.equal(bucketIndexFromPointer(160, rect, 200), 100);
  assert.equal(bucketIndexFromPointer(900, rect, 200), 199);

  const below = positionTooltip(rect, { width: 500, height: 80 }, { width: 320, height: 480 });
  assert.deepEqual(below, { left: 8, top: 42, placement: 'below' });
  const above = positionTooltip(
    { left: 290, top: 420, width: 20, height: 30 },
    { width: 160, height: 70 },
    { width: 320, height: 480 },
  );
  assert.deepEqual(above, { left: 152, top: 342, placement: 'above' });
});

test('请求失败会标记旧数据为 stale，并在恢复后清除', () => {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024, innerHeight: 768 } });
  renderer.render(statusData(service()));
  renderer.renderError(new Error('offline'));

  assert.ok(document.getElementById('status-shell').classList.contains('stale'));
  assert.match(document.getElementById('banner-out').textContent, /showing data from .* retrying/);
  assert.match(document.getElementById('updated').textContent, /^stale/);

  renderer.render(statusData(service()));
  assert.equal(document.getElementById('status-shell').classList.contains('stale'), false);
  assert.doesNotMatch(document.getElementById('updated').textContent, /^stale/);

  const firstFailureDocument = createStatusDocument();
  createStatusRenderer({ document: firstFailureDocument }).renderError(new Error('offline'));
  assert.equal(firstFailureDocument.getElementById('updated').textContent, 'unavailable');
});

test('轮询重绘不会重复广播相同状态', () => {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024, innerHeight: 768 } });
  const data = statusData(service());

  renderer.render(data);
  const firstAnnouncement = document.getElementById('status-announcer').childNodes[0];
  renderer.render({ ...data, generated_at: GENERATED_AT + 5 });
  assert.equal(document.getElementById('status-announcer').childNodes[0], firstAnnouncement);
  renderer.renderError(new Error('offline'));
  const failureAnnouncement = document.getElementById('status-announcer').childNodes[0];
  renderer.renderError(new Error('offline'));
  assert.equal(document.getElementById('status-announcer').childNodes[0], failureAnnouncement);
});

test('总体状态不变时仍广播具体服务变化且只广播一次', () => {
  const onlineResult = { ts: GENERATED_AT - 30, ok: true, latency_ms: 10 };
  const failingResult = { ts: GENERATED_AT - 30, ok: false, latency_ms: 20 };
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024, innerHeight: 768 } });
  const initial = statusData([
    service({ id: 'alpha', model: 'alpha', history: [onlineResult], last: onlineResult }),
    service({ id: 'beta', model: 'beta', history: [failingResult], last: failingResult }),
  ]);
  renderer.render(initial);

  const swapped = {
    ...initial,
    generated_at: GENERATED_AT + 5,
    services: [
      service({ id: 'alpha', model: 'alpha', history: [failingResult], last: failingResult }),
      service({ id: 'beta', model: 'beta', history: [onlineResult], last: onlineResult }),
    ],
  };
  renderer.render(swapped);
  const announcer = document.getElementById('status-announcer');
  assert.match(announcer.textContent, /alpha is now failing/);
  assert.match(announcer.textContent, /beta is now online/);
  const changeAnnouncement = announcer.childNodes[0];

  renderer.render({ ...swapped, generated_at: GENERATED_AT + 10 });
  assert.equal(announcer.childNodes[0], changeAnnouncement);
});

test('轮询采用服务端 refresh_sec 并在每次响应后更新间隔', async () => {
  const scheduled = [];
  const responses = [
    { page: { refresh_sec: 7 } },
    { page: { refresh_sec: 2 } },
  ];
  const poller = createStatusPoller({
    fetchStatus: async () => responses.shift(),
    render() {},
    renderError(error) { throw error; },
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    cancel() {},
  });

  await poller.start();
  assert.equal(scheduled.shift().delay, 7000);
  await poller.refresh();
  assert.equal(scheduled.at(-1).delay, 2000);
  poller.stop();
});

test('重复启动轮询器会复用当前请求', async () => {
  let fetchCount = 0;
  let resolveFetch;
  const scheduled = [];
  const poller = createStatusPoller({
    fetchStatus() {
      fetchCount++;
      return new Promise(resolve => { resolveFetch = resolve; });
    },
    render() {},
    renderError(error) { throw error; },
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    cancel() {},
  });

  const firstStart = poller.start();
  const duplicateStart = poller.start();
  assert.equal(duplicateStart, firstStart);
  assert.equal(fetchCount, 1);

  resolveFetch({ page: { refresh_sec: 5 } });
  await Promise.all([firstStart, duplicateStart]);
  assert.equal(scheduled.length, 1);
  poller.stop();
});

test('停止后已入队的轮询回调不会再发请求', async () => {
  const scheduled = [];
  let fetchCount = 0;
  const poller = createStatusPoller({
    fetchStatus: async () => {
      fetchCount++;
      return { page: { refresh_sec: 5 } };
    },
    render() {},
    renderError(error) { throw error; },
    schedule(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancel() {},
  });

  await poller.start();
  assert.equal(fetchCount, 1);
  const queuedCallback = scheduled.shift();
  poller.stop();
  await queuedCallback();
  assert.equal(fetchCount, 1);
});

test('较慢的旧请求不能覆盖较新的刷新结果', async () => {
  const pending = [];
  const rendered = [];
  const poller = createStatusPoller({
    fetchStatus: () => new Promise(resolve => pending.push(resolve)),
    render(data) { rendered.push(data.version); },
    renderError(error) { throw error; },
    schedule() { return 1; },
    cancel() {},
  });

  const oldRequest = poller.start();
  const newRequest = poller.refresh();
  pending[1]({ version: 'new', page: { refresh_sec: 5 } });
  await newRequest;
  pending[0]({ version: 'old', page: { refresh_sec: 5 } });
  await oldRequest;

  assert.deepEqual(rendered, ['new']);
  poller.stop();
});
