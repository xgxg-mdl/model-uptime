import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildTimeBuckets,
  createStatusPoller,
  createStatusRenderer,
  formatTimeShort,
  resultStatus,
  timeBucketAxisLabels,
} from '../../internal/httpserver/web/assets/scripts/status-page.js';
import { createStatusDocument, findAll } from './helpers/fake-dom.js';

function statusData(service, page = {}) {
  return {
    generated_at: 420,
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
    warning_sec: 30,
    observed_since: 100,
    uptime_pct: 99.9,
    history: [{ ts: 355, started_at: 350, ok: true, latency_ms: 12 }],
    pauses: [],
    last: { ts: 355, started_at: 350, ok: true, latency_ms: 12 },
    ...overrides,
  };
}

function render(data) {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data);
  return { document, renderer };
}

test('完整时间桶使用固定边界并接收不同启动相位的观测周期', () => {
  const buckets = buildTimeBuckets(service({
    observed_since: 145,
    history: [145, 205, 265, 325, 385].map(startedAt => ({
      ts: startedAt + 5, started_at: startedAt, ok: true, latency_ms: 10,
    })),
  }), 5, 420);

  assert.deepEqual(buckets.map(bucket => bucket.kind), ['ok', 'ok', 'ok', 'ok', 'ok']);
  assert.deepEqual(buckets.map(bucket => [bucket.from, bucket.to]), [
    [120, 180], [180, 240], [240, 300], [300, 360], [360, 420],
  ]);
});

test('当前 interval 内刷新时保持完整时间桶边界固定', () => {
  const early = buildTimeBuckets(service(), 5, 421);
  const late = buildTimeBuckets(service(), 5, 479);
  assert.deepEqual(
    early.map(bucket => [bucket.from, bucket.to]),
    late.map(bucket => [bucket.from, bucket.to]),
  );
});

test('完整时间桶区分启动前、暂停、缺失并聚合同桶最严重结果', () => {
  const buckets = buildTimeBuckets(service({
    observed_since: 170,
    history: [
      { ts: 195, started_at: 190, ok: true, latency_ms: 10 },
      { ts: 255, started_at: 250, ok: true, latency_ms: 20 },
      { ts: 255, started_at: 250, ok: false, latency_ms: 20 },
    ],
    pauses: [{ from: 300, to: 360 }],
  }), 5, 420);

  assert.deepEqual(buckets.map(bucket => bucket.kind), [
    'not-started', 'ok', 'bad', 'paused', 'unobserved',
  ]);
  assert.equal(buckets[2].results.length, 3);
  assert.equal(buckets[2].result.ok, false);
});

test('跨过完整桶的在途请求显示 probing 而不是 no data', () => {
  const buckets = buildTimeBuckets(service({
    history: [{ ts: 315, started_at: 300, ok: true, latency_ms: 15_000 }],
    current_probe_started_at: 400,
  }), 5, 480);

  assert.equal(buckets.at(-1).kind, 'probing');
  assert.equal(buckets.at(-1).probeStartedAt, 400);
});

test('已完成探测覆盖请求耗时和正常调度间隔，不制造空桶', () => {
  const buckets = buildTimeBuckets(service({
    observed_since: 120,
    history: [
      { ts: 190, started_at: 120, ok: true, latency_ms: 70_000 },
      { ts: 260, started_at: 191, ok: true, latency_ms: 69_000 },
      { ts: 330, started_at: 261, ok: true, latency_ms: 69_000 },
      { ts: 400, started_at: 331, ok: true, latency_ms: 69_000 },
    ],
  }), 5, 420);

  assert.ok(buckets.every(bucket => bucket.kind !== 'unobserved'));
});

test('完整 interval 没有观测覆盖时仍保留真正的 no data', () => {
  const buckets = buildTimeBuckets(service({
    observed_since: 120,
    history: [
      { ts: 130, started_at: 120, ok: true, latency_ms: 10_000 },
      { ts: 310, started_at: 300, ok: true, latency_ms: 10_000 },
    ],
  }), 5, 420);

  assert.deepEqual(buckets.map(bucket => bucket.kind), [
    'ok', 'unobserved', 'unobserved', 'ok', 'unobserved',
  ]);
});

test('底部刻度使用固定时间桶的真实边界时间', () => {
  const buckets = buildTimeBuckets(service({ history: [] }), 4, 420);
  const boundaries = [180, 240, 300, 360, 420];
  assert.deepEqual(
    timeBucketAxisLabels(buckets),
    boundaries.map(timestamp => formatTimeShort(timestamp).slice(0, 5)),
  );
});

test('成功探测仅在耗时严格超过阈值时进入 warning', () => {
  assert.equal(resultStatus({ ok: true, latency_ms: 30_000 }, 30), 'ok');
  assert.equal(resultStatus({ ok: true, latency_ms: 30_001 }, 30), 'warning');
  assert.equal(resultStatus({ ok: false, latency_ms: 31_000 }, 30), 'bad');

});

test('状态页缺少顶部注释配置时使用两页共享默认值', () => {
  const { document } = render(statusData(service(), { probe_comment: '' }));
  assert.equal(document.getElementById('probe-comment').textContent, '# model-uptime · service health and performance');
});

test('慢响应在服务状态、历史条、耗时和总览中显示 warning', () => {
  const slow = service({
    history: [{ ts: 355, started_at: 350, ok: true, latency_ms: 30_001 }],
    last: { ts: 355, started_at: 350, ok: true, latency_ms: 30_001 },
  });
  const { document } = render(statusData(slow));
  const output = document.getElementById('svc-out');
  const warningBar = findAll(output, element => element.classList.contains('warning'))[0];

  assert.ok(warningBar);
  assert.match(output.textContent, /slow/);
  assert.match(document.getElementById('banner-out').textContent, /warning/);
  assert.ok(findAll(output, element => element.classList.contains('warn') && element.textContent === '30001ms').length > 0);

  warningBar.dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /WARNING/);
});

test('状态页严格渲染完整时间桶并按观测覆盖统计 samples', () => {
  const partial = service({
    observed_since: 170,
    history: [
      { ts: 195, started_at: 190, ok: true, latency_ms: 10 },
      { ts: 255, started_at: 250, ok: false, latency_ms: 20 },
    ],
    pauses: [{ from: 300, to: 360 }],
    last: { ts: 255, started_at: 250, ok: false, latency_ms: 20 },
  });
  const { document } = render(statusData(partial));
  const bars = findAll(document.getElementById('svc-out'), element => element.classList.contains('bar'));
  assert.deepEqual(bars.map(bar => bar.className), [
    'bar not-started', 'bar ok', 'bar bad', 'bar paused', 'bar unobserved',
  ]);
  assert.match(document.getElementById('svc-out').textContent, /samples 2\/5/);

  bars[4].dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /NO DATA/);
});

test('状态页把服务字段和错误详情作为纯文本渲染', () => {
  const injectedName = '<img src=x onerror="globalThis.pwned=true">';
  const injectedError = '<script>globalThis.pwned=true</script>';
  const failing = service({
    name: injectedName,
    model: injectedName,
    history: [{ ts: 355, started_at: 350, ok: false, latency_ms: 18, error: injectedError }],
    last: { ts: 355, started_at: 350, ok: false, latency_ms: 18, error: injectedError },
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
