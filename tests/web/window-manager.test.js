import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { clampWindowPosition, nextWindowMode } from '../../internal/httpserver/web/assets/scripts/window-manager.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const webRoot = path.join(root, 'internal/httpserver/web');
const foundationCSS = fs.readFileSync(path.join(webRoot, 'assets/styles/foundation.css'), 'utf8');
const pageSources = [
  ['status', fs.readFileSync(path.join(webRoot, 'index.html'), 'utf8')],
  ['heatmap', fs.readFileSync(path.join(webRoot, 'heatmap/index.html'), 'utf8')],
  ['admin', fs.readFileSync(path.join(webRoot, 'admin/index.html'), 'utf8')],
];

test('窗口状态按 macOS 控制按钮语义切换', () => {
  assert.equal(nextWindowMode('normal', 'minimize'), 'minimized');
  assert.equal(nextWindowMode('minimized', 'minimize'), 'normal');
  assert.equal(nextWindowMode('normal', 'maximize'), 'maximized');
  assert.equal(nextWindowMode('maximized', 'maximize'), 'normal');
  assert.equal(nextWindowMode('minimized', 'maximize'), 'maximized');
});

test('拖动窗口始终收进桌面和移动视口', () => {
  assert.deepEqual(
    clampWindowPosition({ x: -100, y: -50, width: 200, height: 120, viewportWidth: 390, viewportHeight: 844 }),
    { x: 8, y: 8 },
  );
  assert.deepEqual(
    clampWindowPosition({ x: 500, y: 900, width: 200, height: 120, viewportWidth: 390, viewportHeight: 844 }),
    { x: 182, y: 716 },
  );
  assert.deepEqual(
    clampWindowPosition({ x: 80, y: 80, width: 500, height: 700, viewportWidth: 320, viewportHeight: 568 }),
    { x: 8, y: 8 },
  );
});

test('状态、热力图和管理页接入同一套窗口控制结构', () => {
  for (const [page, source] of pageSources) {
    assert.match(source, /data-window-main/, `${page} 缺少主窗口声明`);
    for (const action of ['close', 'minimize', 'maximize']) {
      assert.match(source, new RegExp(`data-window-action="${action}"`), `${page} 缺少 ${action} 控制`);
    }
    assert.match(source, /id="window-dock"/, `${page} 缺少窗口恢复区`);
  }

  for (const marker of [
    '--window-control-inactive:',
    '.mac-window.is-active .window-control.close::after',
    '.window-controls:hover .window-control.close::after',
    'width: 12px;',
    '@keyframes window-open',
    '.popup-window.is-maximized',
    '.window-dock',
  ]) {
    assert.ok(foundationCSS.includes(marker), `共享基础层缺少窗口规则: ${marker}`);
  }
});
