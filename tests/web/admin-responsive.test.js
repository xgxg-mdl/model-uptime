import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  renderServiceListItem,
  renderServiceTableRow,
} from '../../internal/httpserver/web/assets/scripts/admin/services.js';
import { passwordCharacterCount } from '../../internal/httpserver/web/assets/scripts/admin/app.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');
const adminAppJS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/scripts/admin/app.js'), 'utf8');
const adminCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/admin.css'), 'utf8');
const foundationCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/foundation.css'), 'utf8');
const statusCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/status.css'), 'utf8');

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
    'class="admin-nav" aria-label="Public pages"',
    '<a href="/heatmap/">heatmap</a>',
    'class="wrap admin-window mac-window hidden"',
    'class="titlebar window-titlebar"',
    'class="window-controls"',
    'class="auth-view subwindow mac-window popup-window hidden" id="login-view"',
    'class="subwindow-title" id="login-title">authenticate · model-uptime',
    'class="auth-terminal-form" id="login-form"',
    'class="auth-input-row"',
    'id="window-layer"',
    'id="window-dock"',
  ]) {
    assert.ok(html.includes(marker), `missing responsive service marker: ${marker}`);
  }
  assert.doesNotMatch(html, /<style\b|<script(?![^>]*\bsrc=)/i);

  for (const marker of [
    '@media (max-width: 959px)',
    '.service-table-wrap { display: none; }',
    '.service-toolbar > #new-btn { grid-column: 3; justify-self: end; }',
    'grid-template-columns: 20px minmax(0, 1fr) auto',
    '.service-item-actions .icon-btn { width: 32px; height: 32px;',
    '@media (max-width: 559px)',
    '.update-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }',
    '.metric-options { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));',
    '.admin-window {',
  ]) {
    assert.ok(adminCSS.includes(marker), `missing responsive admin style: ${marker}`);
  }
  assert.ok(foundationCSS.includes('.mac-window.is-active .window-control.close::after'));
  assert.doesNotMatch(adminCSS, /@media \(max-width: 960px\)|content:\s*attr\(data-label\)/);
  assert.doesNotMatch(adminCSS, /\.admin-surface::before|repeating-linear-gradient/);
  assert.match(adminCSS, /\.btn\.primary:disabled\s*{[^}]*background:\s*var\(--surface-raised\);/s);
});

test('登录密码自动填充保持暗色主题', () => {
  assert.match(adminCSS, /\.auth-card input:-webkit-autofill/);
  assert.match(adminCSS, /-webkit-text-fill-color:\s*var\(--fg\)/);
  assert.match(adminCSS, /-webkit-box-shadow:\s*0 0 0 1000px var\(--term-bg\) inset/);
});

test('认证页和管理页保持清晰的视觉层级', () => {
  for (const marker of ['.auth-card {', '.auth-view {', '.admin-nav a {']) {
    assert.ok(adminCSS.includes(marker), `missing admin visual marker: ${marker}`);
  }
  assert.doesNotMatch(adminCSS, /counter-(?:reset|increment)|counter\(admin-panel\)/);
  for (const removedMarker of ['product-mark', 'auth-identity', 'auth-monogram', 'auth-signal']) {
    assert.ok(!html.includes(removedMarker), `unexpected avatar marker in HTML: ${removedMarker}`);
    assert.ok(!adminCSS.includes(removedMarker), `unexpected avatar marker in CSS: ${removedMarker}`);
  }
  for (const removedCopy of ['Welcome back', 'restricted access', 'Stored for this tab session only.']) {
    assert.ok(!html.includes(removedCopy), `unexpected decorative login copy: ${removedCopy}`);
  }
  assert.match(html, /<span class="prompt">~ \$<\/span> admin login <span class="flag">--session<\/span>/);
  assert.match(html, /<span class="prompt">~ \$<\/span> admin setup <span class="flag">--persist<\/span>/);
  assert.match(html, /id="login-token"[^>]*data-window-initial-focus[^>]*aria-label="Admin password"/);
  assert.match(html, /class="btn primary" type="submit">enter<\/button>/);
  assert.match(html, /class="auth-terminal-form vertical" id="setup-form"/);
  assert.doesNotMatch(html, />Create administrator<|>Admin password<\/label>|>New admin password<\/label>/);
  assert.match(adminCSS, /\.auth-view\s*{[^}]*width:\s*min\(440px,/s);
  assert.match(adminCSS, /\.auth-window-body\s*{[^}]*background:\s*var\(--term-bg\);/s);
  assert.match(html, /class="wrap admin-window mac-window hidden"[^>]*data-window-id="admin"/);
  assert.match(adminAppJS, /function enterApp\(\)[\s\S]*?windows\.open\('admin'\);/);

  assert.match(adminCSS, /\.admin-nav a\s*{[^}]*text-decoration:\s*none;/s);
  assert.match(
    adminCSS,
    /\.admin-nav a:focus-visible\s*{[^}]*outline:\s*none;[^}]*box-shadow:\s*inset 0 0 0 1px var\(--info-border\);/s,
  );

  const mobileOffset = adminCSS.indexOf('@media (max-width: 559px)');
  const mobileCSS = adminCSS.slice(mobileOffset);
  assert.match(mobileCSS, /\.auth-input-row \{ grid-template-columns: 1fr;/);
});

test('弹窗复选字段使用标题、控件和说明三层布局', () => {
  assert.match(
    html,
    /class="field llm checkbox-field">\s*<label for="f-stream">Streaming<\/label>\s*<div class="checkbox-control">\s*<input type="checkbox" id="f-stream" checked \/>\s*<\/div>\s*<span class="hint">Validate the SSE streaming path by default<\/span>/,
  );
  assert.match(
    html,
    /class="field checkbox-field">\s*<label for="f-enabled">Enabled<\/label>\s*<div class="checkbox-control">\s*<input type="checkbox" id="f-enabled" checked \/>\s*<\/div>/,
  );
  assert.match(
    html,
    /class="field checkbox-field">\s*<label for="tg-enabled">Enabled<\/label>\s*<div class="checkbox-control">\s*<input id="tg-enabled" type="checkbox" checked \/>\s*<\/div>/,
  );
  assert.match(adminCSS, /\.checkbox-control\s*{[^}]*min-height:\s*34px;[^}]*align-items:\s*center;/s);
  assert.match(
    adminCSS,
    /\.checkbox-control > input\[type="checkbox"\]\s*{[^}]*width:\s*14px;[^}]*height:\s*14px;[^}]*margin:\s*0;/s,
  );
  assert.match(adminCSS, /\.field textarea\s*{[^}]*resize:\s*none;/s);
  assert.doesNotMatch(adminCSS, /\.field input,\s*\n\.field select/);
});

test('小屏编辑器保留站点统一的浮动窗口样式', () => {
  assert.match(
    foundationCSS,
    /\.popup-window\s*{[^}]*position:\s*absolute;[^}]*width:\s*min\(720px, calc\(100% - 24px\)\);[^}]*max-height:\s*calc\(100dvh - 24px\);[^}]*transform:\s*translate\(-50%, -50%\);/s,
  );
  assert.match(
    foundationCSS,
    /\.popup-window > :not\(\.window-titlebar\)\s*{[^}]*max-height:\s*calc\(100dvh - 54px\);[^}]*overflow:\s*auto;/s,
  );
  assert.doesNotMatch(
    adminCSS,
    /@media \(max-width: 640px\)[\s\S]*?\.popup-window:is\(\.service-editor, \.bulk-editor, \.subscription-editor\)/,
  );
  assert.doesNotMatch(adminCSS, /\.popup-window[^}]*border-radius:\s*0/);
});

test('toast 提供统一状态视觉和无障碍容器', () => {
  assert.match(html, /id="toast" role="status" aria-live="polite" aria-atomic="true" aria-hidden="true"/);
  assert.match(html, /id="page-load-status" role="alert" aria-live="assertive" aria-atomic="true"/);
  assert.match(html, /role="status" aria-live="polite" aria-atomic="true">\s*<div class="update-grid">/);
  for (const marker of ['.toast.success', '.toast.warning', '.toast.error', '.toast::before']) {
    assert.ok(adminCSS.includes(marker), `missing toast style: ${marker}`);
  }
  assert.match(adminCSS, /\.toast \{[^}]*visibility:\s*hidden;/s);
  assert.match(adminCSS, /\.toast\.show \{[^}]*visibility:\s*visible;/s);
});

test('管理密码长度按 Unicode 字符而非编码单元计算', () => {
  assert.equal(passwordCharacterCount('密码密码'), 4);
  assert.equal(passwordCharacterCount('密码密码密码密码'), 8);
  assert.equal(passwordCharacterCount('🔐🔐🔐🔐🔐🔐🔐🔐'), 8);
});

test('所有视口的滚动都收进管理窗口内容区', () => {
  const desktopOffset = adminCSS.indexOf('@media (min-width: 960px)');
  assert.ok(desktopOffset >= 0, 'missing desktop admin layout');
  const layoutCSS = adminCSS;

  for (const marker of [
    'height: 100dvh;',
    'overflow: hidden;',
    'display: flex;',
    'flex-direction: column;',
    'height: calc(100dvh - 80px);',
    'flex: 1 1 auto;',
    'overflow-y: auto;',
    'overscroll-behavior: contain;',
  ]) {
    assert.ok(layoutCSS.includes(marker), `missing window scrolling style: ${marker}`);
  }
  assert.match(layoutCSS, /\.admin-window\s*{[^}]*height:\s*calc\(100dvh - 80px\);/s);
  assert.match(layoutCSS, /@media \(max-width: 959px\)[\s\S]*?\.admin-window\s*{[^}]*height:\s*calc\(100dvh - 40px\);/);
  assert.match(foundationCSS, /@media \(max-width: 640px\)[\s\S]*?--page-padding:\s*16px 12px 24px;/);
  assert.doesNotMatch(layoutCSS, /\.admin-surface\s*{[^}]*\n\s*height:\s*calc\(100% - 30px\);/);
  assert.doesNotMatch(layoutCSS, /scrollbar-gutter/);
  assert.match(foundationCSS, /scrollbar-color: var\(--scroll-thumb\) transparent;/);
  assert.match(foundationCSS, /\*::-webkit-scrollbar \{ width: 8px; height: 8px; \}/);
  assert.doesNotMatch(`${adminCSS}\n${statusCSS}`, /scrollbar-(?:color|width)|::-webkit-scrollbar/);
  assert.match(foundationCSS, /@media \(max-width: 640px\)[\s\S]*?\* \{ scrollbar-width: none; \}/);
  assert.match(foundationCSS, /@media \(max-width: 640px\)[\s\S]*?\*::-webkit-scrollbar \{ display: none; \}/);
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
  const foundationMobileOffset = foundationCSS.indexOf('@media (max-width: 640px)');
  const foundationBodyOffset = foundationCSS.indexOf('\nbody {\n  padding:');
  const bodyRule = cssRule(foundationCSS, 'body', foundationBodyOffset);
  const itemRule = cssRule(adminCSS, '.service-item');
  const metadataRule = cssRule(adminCSS, '.service-item-meta');
  const itemActionsRule = cssRule(adminCSS, '.service-item-actions');
  const iconRule = cssRule(adminCSS, '.service-item-actions .icon-btn');
  const actionsRule = cssRule(adminCSS, '.service-item-actions .actions');
  const mobileRootRule = cssRule(foundationCSS, ':root', foundationMobileOffset);
  const mobilePanelBodyRule = cssRule(adminCSS, '.panel-body', mobileOffset);
  const panelRule = cssRule(adminCSS, '.panel');

  const estimatedRowHeight =
    cssNumber(bodyRule, /font-size:\s*(\d+)px/, 'body font size') *
      cssNumber(bodyRule, /line-height:\s*([\d.]+)/, 'body line height') +
    cssNumber(metadataRule, /font-size:\s*(\d+)px/, 'metadata font size') *
      cssNumber(metadataRule, /line-height:\s*([\d.]+)/, 'metadata line height') +
    cssNumber(itemRule, /padding:\s*(\d+)px\s+0/, 'service row padding') * 2 +
    cssNumber(itemRule, /row-gap:\s*(\d+)px/, 'service row gap') * 2 +
    cssNumber(itemActionsRule, /padding-top:\s*(\d+)px/, 'action top padding') +
    cssNumber(iconRule, /height:\s*(\d+)px/, 'action height');
  assert.ok(estimatedRowHeight <= 110, `mobile service row density regressed to ${estimatedRowHeight}px`);

  const actionGroupWidth =
    4 * cssNumber(iconRule, /width:\s*(\d+)px/, 'action width') +
    3 * cssNumber(actionsRule, /gap:\s*(\d+)px/, 'action gap');
  const bodyHorizontalPadding = cssNumber(
    mobileRootRule,
    /--page-padding:\s*\d+px\s+(\d+)px/,
    'mobile body horizontal padding',
  );
  const panelBorder = cssNumber(panelRule, /border:\s*(\d+)px/, 'panel border');
  const panelBodyPadding = cssNumber(mobilePanelBodyRule, /padding:\s*(\d+)px/, 'mobile panel padding');
  const selectionColumn = cssNumber(itemRule, /grid-template-columns:\s*(\d+)px/, 'selection column');
  const columnGap = cssNumber(itemRule, /column-gap:\s*(\d+)px/, 'service column gap');

  for (const viewportWidth of [320, 390]) {
    const panelContentWidth = viewportWidth - 2 * bodyHorizontalPadding - 2 * panelBorder - 2 * panelBodyPadding;
    assert.ok(actionGroupWidth <= panelContentWidth - selectionColumn - columnGap);
  }
});

test('管理页颜色只来自已审核的共享色板', () => {
  const allowedColors = new Set([
    '#000',
    '#0d0d10',
    '#141418',
    '#15151a',
    '#1a1a1e',
    '#222228',
    '#24242a',
    '#26262d',
    '#2a2a30',
    '#3a3a42',
    '#55555e',
    '#66666d',
    '#5eff9c',
    '#7afcff',
    '#8a8a94',
    '#d4d4d4',
    '#febc2e',
    '#ff5e7a',
    '#ff5f57',
    '#ffc857',
    '#28c840',
    '#7a1712',
    '#805b0b',
    '#0b6719',
  ]);
  const source = `${foundationCSS}\n${adminCSS}`;
  const unexpected = [...new Set(source.match(/#[0-9a-fA-F]{3,8}\b/g) || [])].filter(
    color => !allowedColors.has(color.toLowerCase()),
  );
  assert.deepEqual(unexpected, []);
});

test('公共终端样式集中在基础层，页面样式只消费颜色变量', () => {
  assert.doesNotMatch(adminCSS, /#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)/);

  for (const selector of ['.titlebar', '.window-controls', '.window-control', '.prompt', '.flag']) {
    assert.ok(foundationCSS.includes(`${selector} {`), `基础层缺少公共规则: ${selector}`);
    assert.ok(!adminCSS.includes(`${selector} {`), `管理页重复声明公共规则: ${selector}`);
    assert.ok(!statusCSS.includes(`${selector} {`), `状态页重复声明公共规则: ${selector}`);
  }

  assert.match(foundationCSS, /@keyframes surface-in/);
  assert.doesNotMatch(adminCSS, /@keyframes surface-in/);
  assert.doesNotMatch(statusCSS, /@keyframes surface-in/);
});

test('所有管理窗口共用站点级窗口结构和真实控制按钮', () => {
  for (const marker of [
    '.subwindow {',
    '.subwindow-titlebar {',
    '.window-controls {',
    '.window-control {',
    '.subwindow-title {',
    '.subwindow-body {',
    '.window-layer {',
    '.popup-window {',
  ]) {
    assert.ok(foundationCSS.includes(marker), '基础层缺少子窗口组件: ' + marker);
  }

  assert.equal((html.match(/class="subwindow-titlebar window-titlebar"/g) || []).length, 5);
  assert.equal((html.match(/class="window-controls"/g) || []).length, 6);
  assert.equal((html.match(/data-window-action="(?:close|minimize|maximize)"/g) || []).length, 18);
  for (const marker of [
    'class="auth-view subwindow mac-window popup-window hidden" id="login-view"',
    'class="auth-view subwindow mac-window popup-window hidden" id="setup-view"',
    'class="service-editor subwindow mac-window popup-window hidden" id="editor"',
    'class="bulk-editor subwindow mac-window popup-window hidden" id="bulk-editor"',
    'class="subscription-editor subwindow mac-window popup-window hidden" id="tg-editor"',
  ]) {
    assert.ok(html.includes(marker), '缺少统一子窗口结构: ' + marker);
  }
});
