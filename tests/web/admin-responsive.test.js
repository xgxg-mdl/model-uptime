import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  renderServiceListItem,
  renderServiceTableRow,
} from '../../internal/httpserver/web/assets/scripts/admin/services.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');
const adminCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/admin.css'), 'utf8');
const foundationCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/foundation.css'), 'utf8');

function cssRule(source, selector, offset = 0) {
  const start = source.indexOf(`${selector} {`, offset);
  const end = source.indexOf('}', start);
  assert.ok(start >= 0 && end >= 0, `CSS rule not found: ${selector}`);
  return source.slice(start, end);
}

function cssNumber(rule, pattern, description) {
  const match = rule.match(pattern);
  assert.ok(match, `CSS value not found: ${description}`);
  return Number(match[1]);
}

test('管理页使用外置资源并保留响应式结构', () => {
  for (const marker of [
    'href="/assets/styles/foundation.css"',
    'href="/assets/styles/admin.css"',
    'type="module" src="/assets/scripts/admin/app.js"',
    'class="service-toolbar"',
    'class="actions service-bulk-actions hidden" id="bulk-actions"',
    'class="service-table-wrap"',
    'class="service-table"',
    'class="service-list" id="svc-list"',
    'class="telegram-token-row"',
    'class="metric-options"',
    'class="wrap admin-window"',
    'class="titlebar"',
    'class="lights" aria-hidden="true"',
    'id="login-title">authentication required',
  ]) {
    assert.ok(html.includes(marker), `missing responsive service marker: ${marker}`);
  }
  assert.doesNotMatch(html, /<style\b|<script(?![^>]*\bsrc=)/i);

  for (const marker of [
    '@media (max-width: 959px)',
    '.service-table-wrap { display: none; }',
    'grid-template-columns: 20px minmax(0, 1fr) auto',
    '.service-item-actions .icon-btn { width: 32px; height: 32px;',
    '@media (max-width: 559px)',
    '.update-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }',
    '.metric-options { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));',
    '.admin-window {',
    '.light.close { background: var(--btn-close); }',
    '.admin-surface::before {',
  ]) {
    assert.ok(adminCSS.includes(marker), `missing responsive admin style: ${marker}`);
  }
  assert.doesNotMatch(adminCSS, /@media \(max-width: 960px\)|content:\s*attr\(data-label\)/);
});

test('登录密码自动填充保持暗色主题', () => {
  assert.match(adminCSS, /\.login-card input:-webkit-autofill/);
  assert.match(adminCSS, /-webkit-text-fill-color:\s*var\(--fg\)/);
  assert.match(adminCSS, /-webkit-box-shadow:\s*0 0 0 1000px #15151a inset/);
});

test('桌面端滚动收进管理窗口内容区', () => {
  const desktopOffset = adminCSS.indexOf('@media (min-width: 960px)');
  assert.ok(desktopOffset >= 0, 'missing desktop admin layout');
  const desktopCSS = adminCSS.slice(desktopOffset);

  for (const marker of [
    'height: 100dvh;',
    'overflow: hidden;',
    'display: flex;',
    'flex-direction: column;',
    'height: auto;',
    'max-height: calc(100dvh - 80px);',
    'flex: 0 1 auto;',
    'flex: 1 1 auto;',
    'overflow-y: auto;',
    'overscroll-behavior: contain;',
    'scrollbar-color: rgba(138, 138, 148, 0.42) transparent;',
    '.admin-content::-webkit-scrollbar { width: 8px; }',
  ]) {
    assert.ok(desktopCSS.includes(marker), `missing desktop scrolling style: ${marker}`);
  }
  assert.doesNotMatch(desktopCSS, /\.admin-window\s*{[^}]*\n\s*height:\s*calc\(100dvh - 80px\);/);
  assert.doesNotMatch(desktopCSS, /\.admin-surface\s*{[^}]*\n\s*height:\s*calc\(100% - 30px\);/);
  assert.doesNotMatch(desktopCSS, /scrollbar-gutter/);
});

test('桌面和移动服务渲染器保持相同操作能力并转义字段', () => {
  const service = {
    id: 'openai-main',
    name: 'Primary <Model>',
    protocol: 'chat',
    model: 'gpt-5',
    provider: 'OpenAI',
    interval_sec: 60,
    enabled: true,
  };

  const listItem = renderServiceListItem(service);
  assert.match(listItem, /chat · gpt-5 · OpenAI/);
  assert.match(listItem, /service-item-interval">60s/);
  assert.match(listItem, /title="Primary &lt;Model&gt; #openai-main"/);
  assert.equal((listItem.match(/data-act=/g) || []).length, 4);
  assert.equal((listItem.match(/class="btn icon-btn/g) || []).length, 4);
  assert.doesNotMatch(listItem, /Primary <Model>/);

  for (const label of ['Edit service', 'Duplicate service', 'Test connection', 'Delete service']) {
    assert.ok(listItem.includes(`title="${label}" aria-label="${label}"`));
  }

  const tableRow = renderServiceTableRow(service);
  assert.match(tableRow, /^<tr data-service-row>/);
  assert.match(tableRow, /<td>chat<\/td>/);
  assert.equal((tableRow.match(/data-act=/g) || []).length, 4);
});

test('移动服务行在窄视口内保持紧凑且操作按钮不溢出', () => {
  const mobileOffset = adminCSS.indexOf('@media (max-width: 559px)');
  const bodyRule = cssRule(adminCSS, 'body');
  const itemRule = cssRule(adminCSS, '.service-item');
  const metadataRule = cssRule(adminCSS, '.service-item-meta');
  const itemActionsRule = cssRule(adminCSS, '.service-item-actions');
  const iconRule = cssRule(adminCSS, '.service-item-actions .icon-btn');
  const actionsRule = cssRule(adminCSS, '.service-item-actions .actions');
  const mobileBodyRule = cssRule(adminCSS, 'body', mobileOffset);
  const mobilePanelBodyRule = cssRule(adminCSS, '.panel-body', mobileOffset);
  const panelRule = cssRule(adminCSS, '.panel');

  const estimatedRowHeight =
    cssNumber(bodyRule, /font-size:\s*(\d+)px/, 'body font size')
      * cssNumber(bodyRule, /line-height:\s*([\d.]+)/, 'body line height')
    + cssNumber(metadataRule, /font-size:\s*(\d+)px/, 'metadata font size')
      * cssNumber(metadataRule, /line-height:\s*([\d.]+)/, 'metadata line height')
    + cssNumber(itemRule, /padding:\s*(\d+)px\s+0/, 'service row padding') * 2
    + cssNumber(itemRule, /row-gap:\s*(\d+)px/, 'service row gap') * 2
    + cssNumber(itemActionsRule, /padding-top:\s*(\d+)px/, 'action top padding')
    + cssNumber(iconRule, /height:\s*(\d+)px/, 'action height');
  assert.ok(estimatedRowHeight <= 110, `mobile service row density regressed to ${estimatedRowHeight}px`);

  const actionGroupWidth =
    4 * cssNumber(iconRule, /width:\s*(\d+)px/, 'action width')
    + 3 * cssNumber(actionsRule, /gap:\s*(\d+)px/, 'action gap');
  const bodyHorizontalPadding = cssNumber(
    mobileBodyRule,
    /padding:\s*\d+px\s+(\d+)px/,
    'mobile body horizontal padding',
  );
  const panelBorder = cssNumber(panelRule, /border:\s*(\d+)px/, 'panel border');
  const panelBodyPadding = cssNumber(mobilePanelBodyRule, /padding:\s*(\d+)px/, 'mobile panel padding');
  const selectionColumn = cssNumber(itemRule, /grid-template-columns:\s*(\d+)px/, 'selection column');
  const columnGap = cssNumber(itemRule, /column-gap:\s*(\d+)px/, 'service column gap');

  for (const viewportWidth of [320, 390]) {
    const panelContentWidth = viewportWidth
      - 2 * bodyHorizontalPadding
      - 2 * panelBorder
      - 2 * panelBodyPadding;
    assert.ok(actionGroupWidth <= panelContentWidth - selectionColumn - columnGap);
  }
});

test('管理页颜色只来自已审核的共享色板', () => {
  const allowedColors = new Set([
    '#000', '#0d0d10', '#141418', '#15151a', '#1a1a1e', '#222228', '#24242a',
    '#26262d', '#2a2a30', '#3a3a42', '#55555e', '#5eff9c', '#7afcff', '#8a8a94',
    '#d4d4d4', '#febc2e', '#ff5e7a', '#ff5f57', '#ffc857', '#28c840',
  ]);
  const source = `${foundationCSS}\n${adminCSS}`;
  const unexpected = [...new Set(source.match(/#[0-9a-fA-F]{3,8}\b/g) || [])]
    .filter(color => !allowedColors.has(color.toLowerCase()));
  assert.deepEqual(unexpected, []);
});
