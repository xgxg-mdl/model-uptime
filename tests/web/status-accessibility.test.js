import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/index.html'), 'utf8');
const foundationCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/foundation.css'), 'utf8');
const statusCSS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/styles/status.css'), 'utf8');
const statusJS = fs.readFileSync(path.join(root, 'internal/httpserver/web/assets/scripts/status-page.js'), 'utf8');

function color(source, variable) {
  const match = source.match(new RegExp(`${variable}:\\s*(#[0-9a-f]{6})`, 'i'));
  assert.ok(match, `missing color token ${variable}`);
  return match[1];
}

function luminance(hex) {
  const channels = hex.match(/[0-9a-f]{2}/gi).map(channel => Number.parseInt(channel, 16) / 255);
  const linear = channels.map(channel => (
    channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  ));
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

function contrast(left, right) {
  const values = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

test('状态页声明正确语言和低噪声动态语义', () => {
  assert.match(html, /<html lang="en">/);
  assert.match(html, /<main class="term" id="status-shell" aria-labelledby="status-title">/);
  assert.match(html, /<h1 class="sr-only" id="status-title">model-uptime status<\/h1>/);
  assert.match(html, /id="banner-out" aria-busy="true"/);
  assert.match(html, /id="status-announcer" role="status" aria-live="polite" aria-atomic="true"/);
  assert.doesNotMatch(html, /id="svc-out"[^>]*aria-live/);
});

test('时间轴用图例、形状和边框共同表达状态', () => {
  for (const label of ['online', 'failing', 'paused', 'no sample']) {
    assert.match(html, new RegExp(`>${label}<`));
  }
  assert.match(statusCSS, /\.bar\.bad\s*\{[^}]*border-top:\s*5px solid var\(--bad\)[^}]*border-bottom:/s);
  assert.match(statusCSS, /\.bar\.paused\s*\{[^}]*border-top:\s*2px dashed[^}]*border-bottom:/s);
  assert.match(statusCSS, /\.bars\s*\{[^}]*height:\s*30px[^}]*touch-action:\s*pan-y/s);
  assert.match(statusCSS, /\.bar\.none\s*\{[^}]*background:\s*transparent/s);
  assert.match(statusCSS, /\.bar\.none:hover\s*\{[^}]*outline:\s*0/s);
  assert.match(statusJS, /bars\.setAttribute\('role', 'listbox'\)/);
  assert.match(statusJS, /bars\.setAttribute\('aria-orientation', 'horizontal'\)/);
  assert.doesNotMatch(statusJS, /detail\.setAttribute\('aria-live'/);
});

test('状态页小字颜色达到 WCAG AA 对比度', () => {
  const terminalBackground = color(foundationCSS, '--term-bg');
  const titlebarBackground = '#2a2a30';
  for (const token of ['--fg-dim', '--fg-mute']) {
    assert.ok(contrast(color(foundationCSS, token), terminalBackground) >= 4.5, `${token} contrast is too low`);
  }
  assert.ok(contrast(color(statusCSS, '--comment'), terminalBackground) >= 4.5);
  assert.ok(contrast(color(statusCSS, '--titlebar-t'), titlebarBackground) >= 4.5);
  assert.ok(contrast('#666672', terminalBackground) >= 3, 'paused timeline contrast is too low');
});

test('tooltip 和长标题在窄视口内允许收缩与换行', () => {
  assert.match(statusCSS, /\.tip\s*\{[^}]*white-space:\s*normal[^}]*max-width:\s*calc\(100vw - 16px\)/s);
  assert.match(statusCSS, /\.titlebar\s*\{[^}]*grid-template-columns:\s*auto minmax\(0, 1fr\) auto/s);
  assert.match(statusCSS, /\.title-text\s*\{[^}]*min-width:\s*0/s);
  assert.match(statusCSS, /\.term\.stale #svc-out\s*\{[^}]*border-left:/s);
});

test('过期与 reduced-motion 状态不降低文字对比度', () => {
  const staleRule = statusCSS.match(/\.term\.stale #svc-out\s*\{([^}]*)\}/)?.[1] || '';
  assert.match(staleRule, /border-left:\s*2px solid var\(--warn\)/);
  assert.doesNotMatch(staleRule, /opacity/);
  assert.doesNotMatch(statusCSS, /\.term\.stale[^{}]*\{[^}]*opacity/s);
  assert.match(statusCSS, /\.bar\.none:hover\s*\{[^}]*outline:\s*0/s);

  const reduced = statusCSS.slice(statusCSS.indexOf('@media (prefers-reduced-motion: reduce)'));
  assert.match(reduced, /\.bar:hover,\s*\.bar\.selected\s*\{\s*transform:\s*none;/s);
});
