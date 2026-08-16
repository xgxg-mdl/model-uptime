import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');
const foundationCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/foundation.css'), 'utf8');
const adminCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/admin.css'), 'utf8');

function color(source, variable) {
  const match = source.match(new RegExp(`${variable}:\\s*(#[0-9a-f]{6})`, 'i'));
  assert.ok(match, `missing color token ${variable}`);
  return match[1];
}

test('管理页使用一致的英文界面并恢复旧版背景', () => {
  assert.match(html, /<html lang="en">/);
  const visibleSource = html.replace(/<!--[\s\S]*?-->/g, '');
  assert.doesNotMatch(visibleSource, /[\u4e00-\u9fff]/);
  assert.doesNotMatch(html, /\sstyle=/i);
  assert.match(adminCSS, /background-image:\s*radial-gradient\(ellipse at top, #26262d 0%, #1a1a1e 60%, #141418 100%\)/);
});

test('所有管理表单控件都有标签，按钮都有显式类型', () => {
  const controls = [...html.matchAll(/<(?:input|select|textarea)\b[^>]*\bid="([^"]+)"[^>]*>/g)];
  for (const [, id] of controls) {
    const explicit = html.includes(`for="${id}"`);
    const wrapped = new RegExp(`<label[^>]*>[\\s\\S]{0,180}?id="${id}"`).test(html);
    assert.ok(explicit || wrapped, `control #${id} has no associated label`);
  }
  assert.doesNotMatch(html, /<button(?![^>]*\btype=)[^>]*>/i);
});

test('编辑器披露状态、动态反馈和登录加载态具备语义', () => {
  for (const marker of [
    'id="auth-loading" role="status"',
    'id="app-view" tabindex="-1" aria-label="Administration"',
    'id="new-btn" aria-controls="editor" aria-expanded="false"',
    'id="bulk-settings" aria-controls="bulk-editor" aria-expanded="false"',
    'id="tg-new-btn" aria-controls="tg-editor" aria-expanded="false"',
    'id="tg-editor" role="region" aria-labelledby="tg-editor-title"',
    'id="test-result" role="status" aria-live="polite" aria-atomic="true"',
    'id="tg-test-result" role="status" aria-live="polite" aria-atomic="true"',
    'id="toast" role="status" aria-live="polite" aria-atomic="true"',
  ]) {
    assert.ok(html.includes(marker), `missing semantic marker: ${marker}`);
  }
});

test('键盘焦点和移动操作密度保持旧版样式', () => {
  assert.match(adminCSS, /\.btn:focus-visible\s*\{\s*outline:\s*2px solid var\(--accent\)/);
  assert.match(adminCSS, /\.field input:focus,[\s\S]*\.field textarea:focus\s*\{\s*outline:\s*none;/);
  const mobile = adminCSS.slice(adminCSS.indexOf('@media (max-width: 959px)'));
  assert.match(mobile, /\.service-item-actions \.icon-btn\s*\{\s*width:\s*32px;\s*height:\s*32px;/);
});

test('管理页恢复 v0.9.0 色阶', () => {
  assert.equal(color(foundationCSS, '--fg'), '#d4d4d4');
  assert.equal(color(foundationCSS, '--fg-dim'), '#8a8a94');
  assert.equal(color(foundationCSS, '--fg-mute'), '#55555e');
});

test('reduced-motion 会逐项关闭管理页动画和过渡', () => {
  const reduced = adminCSS.slice(adminCSS.indexOf('@media (prefers-reduced-motion: reduce)'));
  assert.match(reduced, /\.page-reveal,[\s\S]*\.panel-reveal,[\s\S]*\.feedback-in\s*\{\s*animation:\s*none;/);
  assert.match(reduced, /\.btn,[\s\S]*\.toast\s*\{\s*transition:\s*none;/);
  assert.match(reduced, /\.btn:active\s*\{\s*transform:\s*none;/);
});
