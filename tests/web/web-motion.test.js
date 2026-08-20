import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { createStatusRenderer } from '../../internal/httpserver/web/assets/scripts/status-page.js';
import { createStatusDocument, findAll } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const foundationCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/foundation.css'), 'utf8');
const adminCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/admin.css'), 'utf8');
const statusCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/status.css'), 'utf8');
const heatmapCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/heatmap.css'), 'utf8');
const statusHTML = fs.readFileSync(path.join(root, 'internal/httpserver/web/index.html'), 'utf8');
const heatmapHTML = fs.readFileSync(path.join(root, 'internal/httpserver/web/heatmap/index.html'), 'utf8');

function service(id, ok) {
  const result = { ts: 100, ok, latency_ms: 10 };
  return {
    id,
    name: 'Same display name',
    provider: 'Same provider',
    model: 'same-model',
    interval_sec: 60,
    uptime_pct: ok ? 100 : 90,
    history: [result],
    pauses: [],
    last: result,
  };
}

function statusData(services) {
  return {
    generated_at: 1_700_000_000,
    all_ok: services.every(item => item.last.ok),
    services,
  };
}

const pageConfig = {
  history_len: 2,
  show_uptime: true,
  show_samples: true,
  show_latency: true,
  show_avg_load: true,
};

test('两页共享动效变量并尊重 reduced-motion', () => {
  for (const token of [
    '--motion-fast: 100ms;',
    '--motion-base: 160ms;',
    '--motion-slow: 240ms;',
    '--motion-ease-out: cubic-bezier(0, 0, .2, 1);',
    '--motion-distance: 4px;',
  ]) {
    assert.ok(foundationCSS.includes(token), `缺少统一动效变量: ${token}`);
  }
  assert.match(adminCSS, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(statusCSS, /@media \(prefers-reduced-motion: reduce\)/);
  assert.ok(foundationCSS.includes('@keyframes surface-in'), '基础层缺少终端窗口进入动效');
  for (const marker of ['.panel-reveal', '.feedback-in', '@keyframes feedback-in']) {
    assert.ok(adminCSS.includes(marker), `管理页缺少交互动效: ${marker}`);
  }
  for (const marker of ['.status-change', '@keyframes status-change']) {
    assert.ok(statusCSS.includes(marker), `状态页缺少状态变化动效: ${marker}`);
  }
  for (const marker of ['.terminal-command-active', '@keyframes terminal-output-in']) {
    assert.ok(statusCSS.includes(marker), `公开页面缺少终端输入动效: ${marker}`);
  }
  assert.match(heatmapCSS, /\.range-change\s*{[^}]*animation:/);
  assert.match(statusHTML, /class="term terminal-intro mac-window is-active"/);
  assert.match(statusHTML, /id="command-uptime"/);
  assert.match(statusHTML, /id="command-monitor"/);
  assert.match(
    statusHTML,
    /class="terminal-command-text"[^>]*>[\s\S]*?--watch[\s\S]*?id="cmd-models"[\s\S]*?<\/span><span class="terminal-typing-cursor"/,
  );
  assert.match(heatmapHTML, /class="term heatmap-term terminal-intro mac-window is-active"/);
  assert.match(heatmapHTML, /id="command-heatmap-monitor"/);
  assert.match(heatmapHTML, /id="command-heatmap"/);
  assert.match(
    heatmapHTML,
    /id="command-heatmap-monitor"[^>]*>[\s\S]*?class="terminal-command-text"[^>]*>[\s\S]*?--watch[\s\S]*?id="cmd-models"[\s\S]*?<\/span><span class="terminal-typing-cursor"/,
  );
  assert.match(statusCSS, /\.terminal-command\s*{[^}]*white-space:\s*normal;/s);
  assert.match(statusCSS, /\.terminal-command-text\s*{[^}]*display:\s*inline;[^}]*white-space:\s*normal;/s);
  assert.match(statusCSS, /\.terminal-intro \.terminal-reveal\s*{[^}]*position:\s*absolute;/s);
  assert.match(statusCSS, /@media \(min-width: 641px\)[\s\S]*?body\s*{[^}]*height:\s*100dvh;[^}]*overflow:\s*hidden;/);
  assert.match(statusCSS, /@media \(min-width: 641px\)[\s\S]*?\.term\s*{[^}]*height:\s*auto;[^}]*max-height:\s*100%;/);
  assert.doesNotMatch(statusCSS, /\.term\s*{[^}]*\n\s*height:\s*100%;/);
  assert.match(statusCSS, /@media \(min-width: 641px\)[\s\S]*?\.body\s*{[^}]*overflow-y:\s*auto;/);
  assert.doesNotMatch(statusCSS, /scrollbar-gutter:/);
  assert.match(foundationCSS, /\*::-webkit-scrollbar\s*{\s*width:\s*8px;\s*height:\s*8px;/);
  assert.match(foundationCSS, /\*::-webkit-scrollbar-thumb\s*{[^}]*background:\s*var\(--scroll-thumb\);/);
  assert.doesNotMatch(statusCSS, /\.bar\s*\{[^}]*animation:/s);
});

test('页面背景由基础层统一提供且不会随内容高度拼接', () => {
  assert.match(foundationCSS, /background-attachment:\s*fixed;/);
  assert.match(foundationCSS, /background-repeat:\s*no-repeat;/);
  assert.match(foundationCSS, /background-size:\s*100%\s+100%;/);
  assert.doesNotMatch(adminCSS, /background-(?:attachment|repeat|size):/);
  assert.doesNotMatch(statusCSS, /background-(?:attachment|repeat|size):/);
});

test('三页终端窗口不叠加 CRT 扫描线', () => {
  assert.doesNotMatch(statusCSS, /\.body::before|repeating-linear-gradient/);
  assert.doesNotMatch(heatmapCSS, /\.body::before|repeating-linear-gradient/);
  assert.doesNotMatch(adminCSS, /\.admin-surface::before|repeating-linear-gradient/);
});

test('首次渲染不播放变化动效，整体状态实际变化时才播放', () => {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({
    document,
    window: { innerWidth: 1024 },
  });
  renderer.renderPage(pageConfig);
  const banner = document.getElementById('banner-out');

  renderer.render(statusData([service('a', true)]));
  assert.equal(findAll(banner, element => element.classList.contains('status-change')).length, 0);

  renderer.render(statusData([service('a', true)]));
  assert.equal(findAll(banner, element => element.classList.contains('status-change')).length, 0);

  renderer.render(statusData([service('a', false)]));
  assert.equal(findAll(banner, element => element.classList.contains('status-change')).length, 1);

  renderer.render(statusData([service('a', false)]));
  assert.equal(findAll(banner, element => element.classList.contains('status-change')).length, 0);

  renderer.render(statusData([service('a', true)]));
  assert.equal(findAll(banner, element => element.classList.contains('status-change')).length, 1);
});

test('同名服务重排后仍按稳定 ID 标记真正变化的服务', () => {
  const document = createStatusDocument();
  const renderer = createStatusRenderer({
    document,
    window: { innerWidth: 1024 },
  });
  renderer.renderPage(pageConfig);
  const output = document.getElementById('svc-out');

  renderer.render(statusData([service('a', true), service('b', false)]));
  assert.equal(findAll(output, element => element.classList.contains('status-change')).length, 0);

  renderer.render(statusData([service('b', false), service('a', false)]));
  const headings = findAll(output, element => element.classList.contains('service-heading'));
  const statusSpans = headings.map(heading => heading.children.at(-1));
  assert.equal(statusSpans[0].classList.contains('status-change'), false);
  assert.equal(statusSpans[1].classList.contains('status-change'), true);
});
