import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildBarEvents,
  createStatusPoller,
  createStatusRenderer,
} from '../../internal/httpserver/web/assets/scripts/status-page.js';
import { createStatusDocument, findAll } from './helpers/fake-dom.js';

function statusData(service, page = {}) {
  return {
    generated_at: 1_700_000_000,
    all_ok: service.last?.ok !== false,
    services: [service],
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
  return {
    id: 'service-1',
    name: 'Primary Model',
    provider: 'OpenAI',
    model: 'gpt-5',
    interval_sec: 60,
    uptime_pct: 99.9,
    history: [{ ts: 100, ok: true, latency_ms: 12 }],
    pauses: [],
    last: { ts: 100, ok: true, latency_ms: 12 },
    ...overrides,
  };
}

function render(data) {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data);
  return { document, renderer };
}

test('buildBarEvents 按时间合并样本和暂停区间', () => {
  const events = buildBarEvents(
    [{ ts: 100, ok: true }, { ts: 300, ok: false }],
    [{ from: 150, to: 250 }],
  );

  assert.deepEqual(events.map(event => [event.ts, event.kind]), [
    [100, 'ok'],
    [250, 'paused'],
    [300, 'bad'],
  ]);
});

test('历史格左侧补透明占位并只保留最近事件', () => {
  const partial = service({
    history: [
      { ts: 100, ok: true, latency_ms: 10 },
      { ts: 300, ok: false, latency_ms: 20 },
    ],
    pauses: [{ from: 150, to: 250 }],
    last: { ts: 300, ok: false, latency_ms: 20 },
  });
  const { document } = render(statusData(partial));
  const bars = findAll(document.getElementById('svc-out'), element => element.classList.contains('bar'));
  assert.deepEqual(bars.map(bar => bar.className), [
    'bar none',
    'bar none',
    'bar ok',
    'bar paused',
    'bar bad',
  ]);

  const overflow = service({
    history: Array.from({ length: 10 }, (_, index) => ({
      ts: index + 1,
      ok: true,
      latency_ms: index,
    })),
    last: { ts: 10, ok: true, latency_ms: 9 },
  });
  const overflowDocument = render(statusData(overflow)).document;
  const overflowBars = findAll(
    overflowDocument.getElementById('svc-out'),
    element => element.classList.contains('bar'),
  );
  assert.equal(overflowBars.length, 5);
  assert.ok(overflowBars.every(bar => bar.classList.contains('ok')));
});

test('状态页把服务字段和错误详情作为纯文本渲染', () => {
  const injectedName = '<img src=x onerror="globalThis.pwned=true">';
  const injectedError = '<script>globalThis.pwned=true</script>';
  const failing = service({
    name: injectedName,
    model: injectedName,
    history: [{ ts: 100, ok: false, latency_ms: 18, error: injectedError }],
    last: { ts: 100, ok: false, latency_ms: 18, error: injectedError },
  });
  const { document } = render(statusData(failing));
  const output = document.getElementById('svc-out');

  assert.match(output.textContent, /<img src=x onerror=/);
  assert.equal(findAll(output, element => ['IMG', 'SCRIPT'].includes(element.tagName)).length, 0);

  const badBar = findAll(output, element => element.classList.contains('bad') && element.tagName === 'BUTTON')[0];
  assert.ok(badBar, '失败样本应渲染为可聚焦按钮');
  badBar.dispatchEvent({ type: 'focus' });

  const tooltip = document.getElementById('tip');
  assert.ok(tooltip.classList.contains('show'));
  assert.match(tooltip.textContent, /<script>globalThis\.pwned=true<\/script>/);
  assert.equal(findAll(tooltip, element => element.tagName === 'SCRIPT').length, 0);
  assert.match(badBar.getAttribute('aria-label'), /FAIL/);

  badBar.dispatchEvent({ type: 'keydown', key: 'Escape' });
  assert.equal(tooltip.classList.contains('show'), false);
  assert.equal(badBar.blurred, true);
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
