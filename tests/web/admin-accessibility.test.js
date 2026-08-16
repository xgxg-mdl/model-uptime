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

function luminance(hex) {
  const values = hex.match(/[0-9a-f]{2}/gi).map(value => Number.parseInt(value, 16) / 255);
  const linear = values.map(value => (
    value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  ));
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

function contrast(left, right) {
  const values = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

test('管理页使用一致的英文界面且不含内联样式', () => {
  assert.match(html, /<html lang="en">/);
  const visibleSource = html.replace(/<!--[\s\S]*?-->/g, '');
  assert.doesNotMatch(visibleSource, /[\u4e00-\u9fff]/);
  assert.doesNotMatch(html, /\sstyle=/i);
  assert.doesNotMatch(adminCSS, /(?:linear|radial)-gradient/);
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

test('键盘焦点和移动触控目标有明确保护', () => {
  assert.match(adminCSS, /\.check-row input:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--accent\)/s);
  assert.match(adminCSS, /\.field input:not\(\[type="checkbox"\]\):focus-visible[^}]*outline:\s*2px solid var\(--accent\)/s);
  assert.match(adminCSS, /\.field\.check-row\s*\{[^}]*flex-direction:\s*row[^}]*align-items:\s*center[^}]*flex-wrap:\s*wrap/s);
  assert.match(adminCSS, /\.field\.check-row \.hint\s*\{[^}]*flex-basis:\s*100%[^}]*padding-left:\s*24px/s);
  const mobile = adminCSS.slice(adminCSS.indexOf('@media (max-width: 959px)'));
  assert.match(mobile, /\.service-item-actions \.icon-btn\s*\{\s*width:\s*44px;\s*height:\s*44px;/);
});

test('管理页正文与辅助文本在实际背景上达到 AA 对比度', () => {
  const foreground = color(foundationCSS, '--fg');
  const dim = color(foundationCSS, '--fg-dim');
  const mute = color(foundationCSS, '--fg-mute');
  for (const background of ['#0d0d10', '#15151a', '#24242a']) {
    assert.ok(contrast(foreground, background) >= 4.5);
    assert.ok(contrast(dim, background) >= 4.5);
  }
  assert.ok(contrast(mute, '#15151a') >= 4.5);
});

test('reduced-motion 会逐项关闭管理页动画和过渡', () => {
  const reduced = adminCSS.slice(adminCSS.indexOf('@media (prefers-reduced-motion: reduce)'));
  assert.match(reduced, /\.page-reveal,[\s\S]*\.panel-reveal,[\s\S]*\.feedback-in\s*\{\s*animation:\s*none;/);
  assert.match(reduced, /\.btn,[\s\S]*\.toast\s*\{\s*transition:\s*none;/);
  assert.match(reduced, /\.btn:active\s*\{\s*transform:\s*none;/);
});
