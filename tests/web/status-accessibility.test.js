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

test('状态页声明正确语言和低噪声动态语义', () => {
  assert.match(html, /<html lang="en">/);
  assert.match(html, /<main class="term" id="status-shell" aria-labelledby="status-title">/);
  assert.match(html, /<h1 class="sr-only" id="status-title">model-uptime status<\/h1>/);
  assert.match(html, /id="banner-out" aria-busy="true"/);
  assert.match(html, /id="status-announcer" role="status" aria-live="polite" aria-atomic="true"/);
  assert.doesNotMatch(html, /id="svc-out"[^>]*aria-live/);
});

test('时间轴保留读屏图例并恢复旧版色块', () => {
  for (const label of ['online', 'failing', 'paused', 'no sample']) {
    assert.match(html, new RegExp(`>${label}<`));
  }
  assert.match(html, /class="timeline-legend sr-only"/);
  assert.match(statusCSS, /\.bar\.bad\s*\{\s*background:\s*var\(--bad\);\s*\}/);
  assert.match(statusCSS, /\.bar\.paused\s*\{\s*background:\s*#3a3a42;\s*\}/);
  assert.match(statusCSS, /\.bars\s*\{[^}]*height:\s*22px/s);
  assert.match(statusCSS, /\.bars\s*\{[^}]*touch-action:\s*pan-y/s);
  assert.match(statusCSS, /\.bar\.none\s*\{[^}]*background:\s*transparent/s);
  assert.match(statusJS, /bars\.setAttribute\('role', 'listbox'\)/);
  assert.match(statusJS, /bars\.setAttribute\('aria-orientation', 'horizontal'\)/);
  assert.doesNotMatch(statusJS, /detail\.setAttribute\('aria-live'/);
});

test('状态页恢复 v0.9.0 色阶', () => {
  assert.equal(color(foundationCSS, '--fg-dim'), '#8a8a94');
  assert.equal(color(foundationCSS, '--fg-mute'), '#55555e');
  assert.equal(color(statusCSS, '--comment'), '#5f6268');
  assert.equal(color(statusCSS, '--titlebar-t'), '#8a8a94');
});

test('tooltip 保持视口夹紧且标题栏恢复旧版布局', () => {
  assert.match(statusCSS, /\.tip\s*\{[^}]*white-space:\s*normal[^}]*max-width:\s*calc\(100vw - 16px\)/s);
  assert.match(statusCSS, /\.titlebar\s*\{[^}]*background:\s*linear-gradient[^}]*grid-template-columns:\s*1fr auto 1fr/s);
  assert.doesNotMatch(statusCSS, /\.term\.stale #svc-out/);
});

test('过期状态不改变外观且 reduced-motion 覆盖选择态', () => {
  assert.doesNotMatch(statusCSS, /\.term\.stale[^{}]*\{[^}]*opacity/s);

  const reduced = statusCSS.slice(statusCSS.indexOf('@media (prefers-reduced-motion: reduce)'));
  assert.match(reduced, /\.bar:hover,\s*\.bar:focus-visible\s*\{\s*transform:\s*none;/s);
  assert.match(reduced, /\.bar\.selected\s*\{\s*transform:\s*none;/s);
});
