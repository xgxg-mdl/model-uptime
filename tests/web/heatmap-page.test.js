import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import {
  axisLabels,
  cellTooltipModel,
  createHeatmapPoller,
  createHeatmapRenderer,
  formatBeijingTime,
  gridLayout,
  normalizeRange,
} from '../../internal/httpserver/web/assets/scripts/heatmap-page.js';
import { createElementDocument, findAll } from './helpers/fake-dom.js';

const html = readFileSync(new URL('../../internal/httpserver/web/heatmap/index.html', import.meta.url), 'utf8');
const css = readFileSync(new URL('../../internal/httpserver/web/assets/styles/heatmap.css', import.meta.url), 'utf8');

function heatmapDocument() {
  const document = createElementDocument([
    'heatmap-out', 'tip', 'term-subtitle', 'probe-comment', 'active-range', 'cmd-models', 'updated', 'login-time',
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
    range: '7d',
    timezone: 'Asia/Shanghai',
    rows: ['08-18'],
    columns: Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, '0')),
    page: { title: 'Status', subtitle: 'model-uptime', probe_comment: 'Monitoring production models' },
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

test('热力图页面保留公开导航和响应式终端结构', () => {
  assert.match(html, /data-range="1d">1d<\/button>/);
  assert.match(html, /data-range="7d">7d<\/button>/);
  assert.match(html, /data-range="30d">30d<\/button>/);
  assert.match(html, /id="cmd-models"/);
  assert.match(html, /id="probe-comment"># …<\/span>/);
  assert.doesNotMatch(html, /Asia\/Shanghai/);
  assert.doesNotMatch(html, /data-range="(?:day|week|month)"/);
  assert.match(html, /href="\/"[^>]*>status<\/a>/);
  assert.match(html, /href="\/admin\/"[^>]*>manage<\/a>/);
  assert.match(css, /\.heatmap-panels\s*{[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(css, /@media \(max-width: 899px\)[^]*\.heatmap-panels\s*{[^}]*grid-template-columns:\s*1fr/);
  assert.match(css, /\.heat-grid\s*{[^}]*width:\s*100%/);
  assert.match(css, /\.heat-row\s*{[^}]*grid-template-columns:\s*repeat\(24, minmax\(0, 1fr\)\)/);
  assert.match(css, /--heat-healthy:\s*var\(--ok\)/);
  assert.match(css, /--heat-warning:\s*var\(--warn\)/);
  assert.match(css, /--heat-failing:\s*var\(--bad\)/);
  assert.match(css, /--heat-empty:\s*#3a3a42/);
  assert.match(css, /\.heatmap-panel \.service-heading\s*{[^}]*margin-top:\s*0/);
  assert.doesNotMatch(css, /\.heatmap-panel\s*{[^}]*(?:border|background):/);
  assert.doesNotMatch(css, /\.heat-cell\.[^{]+{[^}]*opacity:/);
  assert.doesNotMatch(css, /\.heat-cell(?:\.[^{]+)?\s*{[^}]*box-shadow:/);
  assert.match(css, /\.heatmap-term \.body::before\s*{\s*display:\s*none/);
});

test('范围和北京时间格式采用稳定默认值', () => {
  assert.equal(normalizeRange('30d'), '30d');
  assert.equal(normalizeRange('month'), '7d');
  assert.equal(normalizeRange('year'), '7d');
  assert.equal(formatBeijingTime(0), '1970-01-01 08:00');
});

test('热力图缺少顶部注释配置时使用两页共享默认值', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data({ page: {} }));
  assert.equal(document.getElementById('probe-comment').textContent, '# model-uptime · service health and performance');
});

test('每种范围只显示五个均匀分布的底部刻度', () => {
  assert.deepEqual(axisLabels('1d'), ['00:00', '06:00', '12:00', '18:00', '24:00']);
  assert.deepEqual(axisLabels('7d'), ['00:00', '06:00', '12:00', '18:00', '24:00']);
  const dates = Array.from({ length: 30 }, (_, index) => `07-${String(index + 1).padStart(2, '0')}`);
  const labels = axisLabels('30d', dates);
  assert.equal(labels.length, 5);
  assert.equal(labels[0], dates[0]);
  assert.equal(labels.at(-1), dates.at(-1));
});

test('三种范围都形成无结构空位的完整二维矩阵', () => {
  const columns = Array.from({ length: 24 }, (_, hour) => String(hour));
  const cases = [
    { range: '1d', rowCount: 4, columnCount: 24, cells: 96 },
    { range: '7d', rowCount: 7, columnCount: 24, cells: 168 },
    { range: '30d', rowCount: 24, columnCount: 30, cells: 720 },
  ];
  for (const item of cases) {
    const rows = Array.from({ length: item.range === '1d' ? 4 : Number.parseInt(item.range, 10) }, (_, index) => String(index));
    const cells = Array.from({ length: item.cells }, (_, sourceIndex) => ({ sourceIndex }));
    const layout = gridLayout(item.range, rows, columns, cells);
    assert.equal(layout.rows.length, item.rowCount);
    assert.equal(layout.columnCount, item.columnCount);
    assert.ok(layout.rows.every(row => row.length === item.columnCount));
    assert.equal(layout.rows.flat().length, item.cells);
  }
  const transposed = gridLayout('30d', Array.from({ length: 30 }, (_, index) => String(index)), columns, Array.from({ length: 720 }, (_, sourceIndex) => ({ sourceIndex })));
  assert.deepEqual(transposed.rows[0].slice(0, 3).map(item => item.sourceIndex), [0, 24, 48]);
  assert.equal(transposed.rows[1][0].sourceIndex, 1);
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
  assert.equal(document.getElementById('probe-comment').textContent, '# Monitoring production models');
  const commandModels = document.getElementById('cmd-models');
  assert.equal(commandModels.textContent, ' gpt-5');
  assert.ok(commandModels.children[0].classList.contains('warn'));

  cells[0].focus();
  const tip = document.getElementById('tip');
  assert.ok(tip.classList.contains('show'));
  assert.match(tip.textContent, /WARNING/);
  assert.match(tip.textContent, /coverage 100\.0% \(60\/60\)/);
  assert.match(tip.textContent, /avg \/ p95 500ms \/ 900ms/);
  assert.match(cells[0].getAttribute('aria-label'), /WARNING/);
  const grid = findAll(output, element => element.getAttribute('role') === 'grid')[0];
  assert.equal(grid.getAttribute('aria-rowcount'), '1');
  assert.equal(grid.getAttribute('aria-colcount'), '24');
  assert.equal(cells.filter(item => item.getAttribute('tabindex') === '0').length, 1);
  assert.ok(cells.every(item => item.getAttribute('role') === 'gridcell'));
  assert.equal(cells[0].getAttribute('aria-colindex'), '1');
  assert.equal(findAll(output, element => element.classList.contains('heat-axis')).length, 0);
  assert.equal(findAll(output, element => element.classList.contains('heat-row-label')).length, 0);
  const axis = findAll(output, element => element.classList.contains('heatmap-axis'))[0];
  assert.equal(axis.children.length, 5);
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
    target.dispatchEvent({ type: 'keydown', key, bubbles: true, preventDefault() { prevented = true; } });
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

test('30d 将日期转为列并按源索引刷新单元格', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1280 } });
  const sourceCells = Array.from({ length: 720 }, (_, index) => cell({
    start_ts: 1_776_500_400 + index * 3600,
    end_ts: 1_776_504_000 + index * 3600,
    status: index === 24 ? 'warning' : 'healthy',
  }));
  const service = { ...data().services[0], cells: sourceCells };
  renderer.render(data({
    range: '30d',
    rows: Array.from({ length: 30 }, (_, index) => `08-${String(index + 1).padStart(2, '0')}`),
    services: [service],
  }));

  const output = document.getElementById('heatmap-out');
  const grid = findAll(output, element => element.getAttribute('role') === 'grid')[0];
  const rows = findAll(grid, element => element.getAttribute('role') === 'row');
  const cells = findAll(grid, element => element.classList.contains('heat-cell'));
  assert.equal(grid.getAttribute('aria-rowcount'), '24');
  assert.equal(grid.getAttribute('aria-colcount'), '30');
  assert.equal(rows.length, 24);
  assert.ok(rows.every(row => row.style.gridTemplateColumns === 'repeat(30, minmax(0, 1fr))'));
  assert.equal(cells.length, 720);
  assert.equal(cells[1].getAttribute('data-cell-index'), '24');
  assert.ok(cells[1].classList.contains('warning'));

  const updatedCells = sourceCells.map((item, index) => index === 24 ? { ...item, status: 'failing' } : item);
  renderer.render(data({
    range: '30d',
    rows: Array.from({ length: 30 }, (_, index) => `08-${String(index + 1).padStart(2, '0')}`),
    services: [{ ...service, cells: updatedCells }],
  }));
  assert.equal(findAll(output, element => element.classList.contains('heat-cell'))[1], cells[1]);
  assert.ok(cells[1].classList.contains('failing'));
});

test('相同网格的数据刷新复用节点并保留焦点', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data());
  const firstRenderCells = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heat-cell'));
  firstRenderCells[7].focus();

  const service = data().services[0];
  renderer.render(data({
    generated_at: 1_776_504_060,
    services: [{
      ...service,
      status: 'failing',
      cells: [cell({ status: 'failing', intensity: 5, failed_samples: 60, healthy_samples: 0 }), ...service.cells.slice(1)],
    }],
  }));
  const secondRenderCells = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heat-cell'));
  assert.equal(secondRenderCells[7], firstRenderCells[7]);
  assert.equal(document.activeElement, secondRenderCells[7]);
  assert.ok(secondRenderCells[0].classList.contains('failing'));
  assert.equal(secondRenderCells[7].getAttribute('tabindex'), '0');
  assert.equal(secondRenderCells.filter(item => item.getAttribute('tabindex') === '0').length, 1);
});

test('格子交互由输出容器统一委托', () => {
  const document = heatmapDocument();
  const renderer = createHeatmapRenderer({ document, window: { innerWidth: 1024 } });
  renderer.render(data());

  const output = document.getElementById('heatmap-out');
  const cells = findAll(output, element => element.classList.contains('heat-cell'));
  assert.ok(cells.every(item => item.listeners.size === 0));
  for (const eventType of ['mouseover', 'mouseout', 'focusin', 'focusout', 'click', 'keydown']) {
    assert.equal(output.listeners.get(eventType)?.length, 1, `${eventType} 应只绑定一次`);
  }

  cells[0].dispatchEvent({ type: 'mouseover', bubbles: true });
  assert.ok(document.getElementById('tip').classList.contains('show'));
  assert.match(document.getElementById('tip').textContent, /WARNING/);
});

test('首次渲染先提交一个模型并逐帧追加其余模型', () => {
  const document = heatmapDocument();
  const scheduled = [];
  const renderer = createHeatmapRenderer({
    document,
    window: { innerWidth: 1024 },
    scheduleFrame(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancelFrame() {},
  });
  const service = data().services[0];
  renderer.render(data({
    services: [
      service,
      { ...service, id: 'service-2', model: 'gpt-5-mini' },
      { ...service, id: 'service-3', model: 'gpt-5-nano' },
    ],
  }));

  const output = document.getElementById('heatmap-out');
  const panelCount = () => findAll(output, element => element.classList.contains('heatmap-panel')).length;
  assert.equal(panelCount(), 1);
  assert.equal(scheduled.length, 1);
  scheduled.shift()();
  assert.equal(panelCount(), 2);
  scheduled.shift()();
  assert.equal(panelCount(), 3);
});

test('30d 单模型按固定格子预算渐进渲染，避免一次创建全部格子', () => {
  const document = heatmapDocument();
  const scheduled = [];
  const renderer = createHeatmapRenderer({
    document,
    window: { innerWidth: 1280 },
    scheduleFrame(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancelFrame() {},
  });
  const service = data().services[0];
  renderer.render(data({
    range: '30d',
    rows: Array.from({ length: 30 }, (_, index) => `08-${String(index + 1).padStart(2, '0')}`),
    services: [{ ...service, cells: Array.from({ length: 720 }, () => cell()) }],
  }));

  const output = document.getElementById('heatmap-out');
  const cellCount = () => findAll(output, element => element.classList.contains('heat-cell')).length;
  assert.equal(cellCount(), 90);
  assert.equal(scheduled.length, 1);
  scheduled.shift()();
  assert.equal(cellCount(), 180);
  while (scheduled.length) scheduled.shift()();
  assert.equal(cellCount(), 720);
  const grid = findAll(output, element => element.getAttribute('role') === 'grid')[0];
  assert.equal(grid.getAttribute('aria-rowcount'), '24');
  assert.equal(grid.getAttribute('aria-colcount'), '30');
});

test('7d 单模型拆成两帧渲染，避免多模型时连续长任务', () => {
  const document = heatmapDocument();
  const scheduled = [];
  const renderer = createHeatmapRenderer({
    document,
    window: { innerWidth: 1280 },
    scheduleFrame(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancelFrame() {},
  });
  const service = data().services[0];
  renderer.render(data({
    rows: Array.from({ length: 7 }, (_, index) => `08-${String(index + 1).padStart(2, '0')}`),
    services: [{ ...service, cells: Array.from({ length: 168 }, () => cell()) }],
  }));

  const output = document.getElementById('heatmap-out');
  const cellCount = () => findAll(output, element => element.classList.contains('heat-cell')).length;
  assert.equal(cellCount(), 96);
  assert.equal(scheduled.length, 1);
  scheduled.shift()();
  assert.equal(cellCount(), 168);
});

test('结构变化取消尚未提交的旧模型', () => {
  const document = heatmapDocument();
  const scheduled = [];
  const renderer = createHeatmapRenderer({
    document,
    window: { innerWidth: 1024 },
    scheduleFrame(callback) {
      const task = { callback, cancelled: false };
      scheduled.push(task);
      return task;
    },
    cancelFrame(task) { task.cancelled = true; },
  });
  const service = data().services[0];
  renderer.render(data({
    services: [service, { ...service, id: 'stale-service', model: 'stale-model' }],
  }));
  renderer.render(data({
    range: '1d',
    services: [{ ...service, id: 'fresh-service', model: 'fresh-model' }],
  }));

  assert.equal(scheduled[0].cancelled, true);
  scheduled[0].callback();
  const panels = findAll(document.getElementById('heatmap-out'), element => element.classList.contains('heatmap-panel'));
  assert.equal(panels.length, 1);
  assert.equal(panels[0].getAttribute('data-service-id'), 'fresh-service');
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
    initialRange: '7d',
    fetchRange: async range => { requested.push(range); return { range }; },
    render: response => rendered.push(response.range),
    renderError: error => { throw error; },
    schedule(callback, delay) { scheduled.push({ callback, delay }); return scheduled.length; },
    cancel() {},
  });

  await poller.start();
  assert.deepEqual(requested, ['7d']);
  assert.equal(scheduled.at(-1).delay, 60_000);
  await poller.setRange('30d');
  assert.deepEqual(rendered, ['7d', '30d']);
  await poller.setVisible(false);
  await poller.setRange('1d');
  assert.deepEqual(requested, ['7d', '30d']);
  await poller.setVisible(true);
  assert.deepEqual(requested, ['7d', '30d', '1d']);
  poller.stop();
});
