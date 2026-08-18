import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import {
  cellTooltipModel,
  createHeatmapPoller,
  createHeatmapRenderer,
  formatBeijingTime,
  normalizeRange,
} from '../../internal/httpserver/web/assets/scripts/heatmap-page.js';
import { createElementDocument, findAll } from './helpers/fake-dom.js';

const html = readFileSync(new URL('../../internal/httpserver/web/heatmap/index.html', import.meta.url), 'utf8');
const css = readFileSync(new URL('../../internal/httpserver/web/assets/styles/heatmap.css', import.meta.url), 'utf8');

function heatmapDocument() {
  const document = createElementDocument([
    'heatmap-out', 'tip', 'term-subtitle', 'active-range', 'updated', 'login-time',
  ]);
  document.getElementById('tip').className = 'tip';
  return document;
}

function cell(overrides = {}) {
  return {
    start_ts: 1_776_500_400,
    end_ts: 1_776_504_000,
    status: 'healthy',
    intensity: 5,
    coverage_pct: 100,
    actual_samples: 60,
    expected_samples: 60,
    healthy_samples: 60,
    warning_samples: 0,
    failed_samples: 0,
    uptime_pct: 100,
    avg_latency_ms: 500,
    p95_latency_ms: 900,
    ...overrides,
  };
}

function data(overrides = {}) {
  return {
    generated_at: 1_776_504_000,
    range: 'week',
    timezone: 'Asia/Shanghai',
    rows: ['08-18'],
    columns: Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, '0')),
    page: { title: 'Status', subtitle: 'model-uptime' },
    services: [{
      id: 'service-1',
      model: 'gpt-5',
      provider: 'OpenAI',
      status: 'warning',
      samples: 60,
      latency_samples: 60,
      uptime_pct: 99.5,
      p95_latency_ms: 1500,
      cells: [
        cell({ status: 'warning', intensity: 2, warning_samples: 12, healthy_samples: 48 }),
        ...Array.from({ length: 23 }, () => cell({ status: 'unobserved', intensity: 0, actual_samples: 0, expected_samples: 0, healthy_samples: 0 })),
      ],
    }],
    ...overrides,
  };
}

test('热力图页面保留公开导航和响应式双列结构', () => {
  assert.match(html, /data-range="day"/);
  assert.match(html, /data-range="week"/);
  assert.match(html, /data-range="month"/);
  assert.match(html, /href="\/"[^>]*>status<\/a>/);
  assert.match(html, /href="\/admin\/"[^>]*>manage<\/a>/);
  assert.match(css, /grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(css, /grid-template-columns:\s*34px repeat\(24, 9px\)/);
  assert.match(css, /@media \(max-width: 760px\)[^]*grid-template-columns:\s*1fr/);
});

test('范围和北京时间格式采用稳定默认值', () => {
  assert.equal(normalizeRange('month'), 'month');
  assert.equal(normalizeRange('year'), 'week');
  assert.equal(formatBeijingTime(0), '1970-01-01 08:00');
});

test('渲染每个模型的二维网格并提供完整 tooltip', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data());

  const output = document.getElementById('heatmap-out');
  assert.equal(findAll(output, element => element.classList.contains('heatmap-panel')).length, 1);
  const cells = findAll(output, element => element.classList.contains('heat-cell'));
  assert.equal(cells.length, 24);
  assert.ok(cells[0].classList.contains('warning'));
  assert.match(output.textContent, /gpt-5/);
  assert.match(output.textContent, /99\.50%/);

  cells[0].dispatchEvent({ type: 'focus' });
  const tip = document.getElementById('tip');
  assert.ok(tip.classList.contains('show'));
  assert.match(tip.textContent, /WARNING/);
  assert.match(tip.textContent, /coverage 100\.0% \(60\/60\)/);
  assert.match(tip.textContent, /avg \/ p95 500ms \/ 900ms/);
  assert.match(cells[0].getAttribute('aria-label'), /WARNING/);
  const grid = findAll(output, element => element.getAttribute('role') === 'grid')[0];
  assert.equal(grid.getAttribute('aria-rowcount'), '1');
  assert.equal(grid.getAttribute('aria-colcount'), '25');
  assert.equal(cells.filter(item => item.getAttribute('tabindex') === '0').length, 1);
  assert.ok(cells.every(item => item.getAttribute('role') === 'gridcell'));
  assert.equal(cells[0].getAttribute('aria-colindex'), '2');
});

test('二维网格使用单一 Tab 入口并支持方向键导航', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  const service = data().services[0];
  renderer.render(data({
    rows: ['08-17', '08-18'],
    services: [{ ...service, cells: [...service.cells, ...service.cells] }],
  }));

  const cells = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heat-cell'));
  assert.equal(cells.length, 48);
  assert.equal(cells.filter(item => item.getAttribute('tabindex') === '0').length, 1);

  const keydown = (target, key) => {
    let prevented = false;
    target.dispatchEvent({ type: 'keydown', key, preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
  };
  cells[0].focus();
  keydown(cells[0], 'ArrowRight');
  assert.equal(document.activeElement, cells[1]);
  keydown(cells[1], 'ArrowDown');
  assert.equal(document.activeElement, cells[25]);
  keydown(cells[25], 'End');
  assert.equal(document.activeElement, cells[47]);
  keydown(cells[47], 'Home');
  assert.equal(document.activeElement, cells[24]);
  keydown(cells[24], 'ArrowUp');
  assert.equal(document.activeElement, cells[0]);
  assert.equal(cells.filter(item => item.getAttribute('tabindex') === '0').length, 1);
});

test('数据刷新后恢复当前模型和网格位置的焦点', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data());
  const firstRenderCells = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heat-cell'));
  firstRenderCells[7].focus();

  renderer.render(data({ generated_at: 1_776_504_060 }));
  const secondRenderCells = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heat-cell'));
  assert.notEqual(secondRenderCells[7], firstRenderCells[7]);
  assert.equal(document.activeElement, secondRenderCells[7]);
  assert.equal(secondRenderCells[7].getAttribute('tabindex'), '0');
  assert.equal(secondRenderCells.filter(item => item.getAttribute('tabindex') === '0').length, 1);
});

test('低覆盖率 tooltip 明确显示 insufficient data', () => {
  const model = cellTooltipModel(cell({
    status: 'insufficient', coverage_pct: 25, actual_samples: 1, expected_samples: 4,
    healthy_samples: 1,
  }));
  assert.equal(model.status, 'INSUFFICIENT DATA');
  assert.deepEqual(model.fields[1], ['coverage', '25.0% (1/4)']);
});

test('轮询切换范围、隐藏暂停、恢复后立即刷新', async () => {
  const requested = [];
  const rendered = [];
  const scheduled = [];
  const poller = createHeatmapPoller({
    initialRange: 'week',
    fetchRange: async range => { requested.push(range); return { range }; },
    render: response => rendered.push(response.range),
    renderError: error => { throw error; },
    schedule(callback, delay) { scheduled.push({ callback, delay }); return scheduled.length; },
    cancel() {},
  });

  await poller.start();
  assert.deepEqual(requested, ['week']);
  assert.equal(scheduled.at(-1).delay, 60_000);
  await poller.setRange('month');
  assert.deepEqual(rendered, ['week', 'month']);
  await poller.setVisible(false);
  await poller.setRange('day');
  assert.deepEqual(requested, ['week', 'month']);
  await poller.setVisible(true);
  assert.deepEqual(requested, ['week', 'month', 'day']);
  poller.stop();
});
