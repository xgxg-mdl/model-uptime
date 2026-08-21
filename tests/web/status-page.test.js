import assert from 'node:assert/strict';
import test from 'node:test';

import {
  axisLabels,
  createStatusPoller,
  createStatusRenderer,
  resultStatus,
  startStatusPage,
} from '../../internal/httpserver/web/assets/scripts/status-page.js';
import { createStatusDocument, findAll } from './helpers/fake-dom.js';

function pageConfig(overrides = {}) {
  return {
    title: 'Status',
    subtitle: 'model-uptime',
    probe_comment: 'checking services',
    history_len: 5,
    refresh_sec: 5,
    show_uptime: true,
    show_samples: true,
    show_latency: true,
    show_avg_load: true,
    ...overrides,
  };
}

function statusData(service) {
  return {
    generated_at: 420,
    all_ok: service.last?.ok !== false,
    services: [service],
  };
}

function timelineSlot(startTimestamp, status, overrides = {}) {
  return {
    start_ts: startTimestamp,
    end_ts: startTimestamp + 60,
    status,
    observation_count: 0,
    ...overrides,
  };
}

function service(overrides = {}) {
  const last = { ts: 355, started_at: 350, ok: true, latency_ms: 12 };
  return {
    name: 'Primary Model',
    provider: 'OpenAI',
    model: 'gpt-5',
    interval_sec: 60,
    warning_sec: 30,
    uptime_pct: 99.9,
    timeline: [
      timelineSlot(120, 'unobserved'),
      timelineSlot(180, 'unobserved'),
      timelineSlot(240, 'unobserved'),
      timelineSlot(300, 'healthy', { observation_count: 1, result: last }),
      timelineSlot(360, 'healthy', { observation_count: 1, result: last }),
    ],
    last,
    ...overrides,
  };
}

function render(data, page = pageConfig()) {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({
    document,
    window: { innerWidth: 1024 },
  });
  renderer.renderPage(page);
  renderer.render(data);
  return { document, renderer };
}

test('状态页底部刻度按 timeline slot 数显示相对分钟或小时', () => {
  assert.deepEqual(axisLabels(60, 60), ['-1h', '-45m', '-30m', '-15m', 'now']);
  assert.deepEqual(axisLabels(60, 300), ['-5h', '-3.8h', '-2.5h', '-1.3h', 'now']);
});

test('成功探测仅在耗时严格超过阈值时进入 warning', () => {
  assert.equal(resultStatus({ ok: true, latency_ms: 30_000 }, 30), 'ok');
  assert.equal(resultStatus({ ok: true, latency_ms: 30_001 }, 30), 'warning');
  assert.equal(resultStatus({ ok: false, latency_ms: 31_000 }, 30), 'bad');
});

test('状态页缺少顶部注释配置时使用两页共享默认值', () => {
  const { document } = render(statusData(service()), pageConfig({ probe_comment: '' }));
  assert.equal(document.getElementById('probe-comment').textContent, '# model-uptime · service health and performance');
});

test('状态数据返回前立即显示独立取得的页面配置', async () => {
  const document = createStatusDocument();
  for (const id of ['terminal', 'command-uptime', 'command-monitor']) document.registerElement(id);
  document.getElementById('terminal').className = 'term terminal-intro';
  const requested = [];
  const poller = startStatusPage({
    document,
    window: { matchMedia: () => ({ matches: false }), innerWidth: 1024 },
    fetch: async url => {
      requested.push(url);
      if (url === '/api/page') {
        return {
          ok: true,
          async json() {
            return pageConfig({
              subtitle: 'Primary monitor',
              probe_comment: 'Checking production models',
            });
          },
        };
      }
      return new Promise(() => {});
    },
    schedule() {
      return 1;
    },
    cancel() {},
    now: () => 420_000,
  });

  await Promise.resolve();
  await Promise.resolve();

  assert.deepEqual(requested, ['/api/page', '/api/status']);
  assert.equal(document.getElementById('probe-comment').textContent, '# Checking production models');
  assert.equal(document.getElementById('term-subtitle').textContent, 'Primary monitor');
  void poller.refresh();
  assert.deepEqual(requested, ['/api/page', '/api/status', '/api/status']);
  poller.stop();
});

test('监控命令显示名称并统一使用参数色，不映射服务可用性', () => {
  const healthy = service({ name: 'GPT 5.6', model: 'gpt-5.6' });
  const failing = service({
    name: 'GPT 5.4 Mini',
    model: 'gpt-5.4-mini',
    last: { ts: 355, started_at: 350, ok: false, latency_ms: 12 },
  });
  const { document } = render({
    ...statusData(healthy),
    services: [healthy, failing],
  });
  const commandModels = document.getElementById('cmd-models');

  assert.equal(commandModels.textContent, ' GPT 5.6 GPT 5.4 Mini');
  assert.ok(commandModels.children.every(model => model.classList.contains('str')));
  assert.equal(
    commandModels.children.some(model => model.classList.contains('ok') || model.classList.contains('bad')),
    false,
  );
});

test('慢响应在服务状态、timeline 状态条、耗时和总览中显示 warning', () => {
  const slowResult = { ts: 355, started_at: 350, ok: true, latency_ms: 30_001 };
  const slow = service({
    timeline: [timelineSlot(360, 'slow', { observation_count: 1, result: slowResult })],
    last: slowResult,
  });
  const { document } = render(statusData(slow));
  const output = document.getElementById('svc-out');
  const warningBar = findAll(output, element => element.classList.contains('warning'))[0];

  assert.ok(warningBar);
  assert.match(output.textContent, /slow/);
  assert.match(document.getElementById('banner-out').textContent, /warning/);
  assert.ok(
    findAll(output, element => element.classList.contains('warn') && element.textContent === '30001ms').length > 0,
  );

  warningBar.dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /WARNING/);
});

test('状态页适配服务端 timeline 状态并按实际 slot 数统计 coverage', () => {
  const healthyResult = { ts: 195, started_at: 190, ok: true, latency_ms: 10 };
  const slowResult = { ts: 255, started_at: 250, ok: true, latency_ms: 30_001 };
  const failingResult = { ts: 315, started_at: 310, ok: false, latency_ms: 20 };
  const projected = service({
    timeline: [
      timelineSlot(120, 'not-started'),
      timelineSlot(180, 'healthy', { observation_count: 1, result: healthyResult }),
      timelineSlot(240, 'slow', { observation_count: 2, result: slowResult }),
      timelineSlot(300, 'failing', { observation_count: 1, result: failingResult }),
      timelineSlot(360, 'probing', { probe_started_at: 381 }),
      timelineSlot(420, 'paused'),
      timelineSlot(480, 'unobserved'),
    ],
    last: failingResult,
  });
  const { document } = render(statusData(projected));
  const bars = findAll(document.getElementById('svc-out'), element => element.classList.contains('bar'));
  assert.deepEqual(
    bars.map(bar => bar.className),
    ['bar not-started', 'bar ok', 'bar warning', 'bar bad', 'bar probing', 'bar paused', 'bar unobserved'],
  );
  assert.match(document.getElementById('svc-out').textContent, /coverage 4\/7/);

  bars[4].dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /PROBING/);

  bars[6].dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /NO DATA/);
});

test('历史结果不会代替服务端 timeline 生成状态条', () => {
  const projected = service({
    timeline: [timelineSlot(300, 'unobserved'), timelineSlot(360, 'unobserved')],
    history: [{ ts: 355, started_at: 120, ok: false, latency_ms: 235_000 }],
  });
  const { document } = render(statusData(projected));
  const output = document.getElementById('svc-out');
  const bars = findAll(output, element => element.classList.contains('bar'));

  assert.deepEqual(
    bars.map(bar => bar.className),
    ['bar unobserved', 'bar unobserved'],
  );
  assert.match(output.textContent, /coverage 0\/2/);
});

test('未知 timeline 状态安全回退为 unobserved', () => {
  const projected = service({
    timeline: [
      timelineSlot(300, 'constructor', {
        observation_count: 1,
        result: { ts: 355, ok: false, latency_ms: 10 },
      }),
      timelineSlot(360, '__proto__'),
    ],
  });
  const { document } = render(statusData(projected));
  const output = document.getElementById('svc-out');
  const bars = findAll(output, element => element.classList.contains('bar'));

  assert.deepEqual(
    bars.map(bar => bar.className),
    ['bar unobserved', 'bar unobserved'],
  );
  assert.match(output.textContent, /coverage 0\/2/);
  bars[0].dispatchEvent({ type: 'focus' });
  assert.match(document.getElementById('tip').textContent, /NO DATA/);
  assert.doesNotMatch(document.getElementById('tip').textContent, /10ms/);
});

test('状态页把服务字段和错误详情作为纯文本渲染', () => {
  const injectedName = '<img src=x onerror="globalThis.pwned=true">';
  const injectedError = '<script>globalThis.pwned=true</script>';
  const failingResult = {
    ts: 355,
    started_at: 350,
    ok: false,
    latency_ms: 18,
    error: injectedError,
  };
  const failing = service({
    name: injectedName,
    model: injectedName,
    timeline: [timelineSlot(360, 'failing', { observation_count: 1, result: failingResult })],
    last: failingResult,
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

test('数据轮询采用独立页面配置提供的刷新间隔', async () => {
  const scheduled = [];
  const poller = createStatusPoller({
    fetchStatus: async () => ({}),
    render() {},
    renderError(error) {
      throw error;
    },
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    cancel() {},
  });

  poller.setRefreshSeconds(7);
  await poller.start();
  assert.equal(scheduled.shift().delay, 7000);
  poller.setRefreshSeconds(2);
  assert.equal(scheduled.at(-1).delay, 2000);
  poller.stop();
});

test('较慢的旧请求不能覆盖较新的刷新结果', async () => {
  const pending = [];
  const rendered = [];
  const poller = createStatusPoller({
    fetchStatus: () => new Promise(resolve => pending.push(resolve)),
    render(data) {
      rendered.push(data.version);
    },
    renderError(error) {
      throw error;
    },
    schedule() {
      return 1;
    },
    cancel() {},
  });

  const oldRequest = poller.start();
  const newRequest = poller.refresh();
  pending[1]({ version: 'new' });
  await newRequest;
  pending[0]({ version: 'old' });
  await oldRequest;

  assert.deepEqual(rendered, ['new']);
  poller.stop();
});
