const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const adminHtml = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');
const statusHtml = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'index.html'), 'utf8');

const sharedTokens = [
  '--motion-fast: 100ms;',
  '--motion-base: 160ms;',
  '--motion-slow: 240ms;',
  '--motion-ease-out: cubic-bezier(0, 0, .2, 1);',
  '--motion-distance: 4px;',
];
for (const [page, source] of [['管理页', adminHtml], ['状态页', statusHtml]]) {
  for (const token of sharedTokens) {
    if (!source.includes(token)) throw new Error(`${page}缺少统一动效变量: ${token}`);
  }
  if (!source.includes('@media (prefers-reduced-motion: reduce)')) {
    throw new Error(`${page}缺少减少动态效果规则`);
  }
}

for (const marker of ['.panel-reveal', '.feedback-in', '@keyframes surface-in', '@keyframes feedback-in']) {
  if (!adminHtml.includes(marker)) throw new Error(`管理页缺少交互动效: ${marker}`);
}
if (/scrollIntoView\(\{\s*behavior:\s*'smooth'/.test(adminHtml)) {
  throw new Error('管理页仍有绕过 reduced-motion 的硬编码平滑滚动');
}
for (const marker of ['.status-change', '@keyframes status-change', 'let previousOverallStatus = null;', 'let previousServiceStates = new Map();']) {
  if (!statusHtml.includes(marker)) throw new Error(`状态页缺少状态变化动效: ${marker}`);
}
if (/\.bar[^}]*animation:/s.test(statusHtml)) {
  throw new Error('轮询重绘的状态条不应重复播放进场动画');
}

function extractFunction(source, name) {
  const start = source.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`未找到函数 ${name}`);
  const bodyStart = source.indexOf('{', start);
  let depth = 0;
  for (let i = bodyStart; i < source.length; i++) {
    if (source[i] === '{') depth++;
    else if (source[i] === '}' && --depth === 0) return source.slice(start, i + 1);
  }
  throw new Error(`函数 ${name} 没有结束`);
}

const banner = { innerHTML: '' };
const context = {
  previousOverallStatus: null,
  document: { getElementById: id => {
    if (id !== 'banner-out') throw new Error(`意外的元素查询: ${id}`);
    return banner;
  } },
  fmtTimeShort: () => '12:00:00',
};
vm.createContext(context);
vm.runInContext(extractFunction(statusHtml, 'renderBanner'), context);

function render(allOK) {
  context.renderBanner({
    all_ok: allOK,
    generated_at: 1,
    services: [{ uptime_pct: 100, last: { ok: allOK } }],
  }, { show_avg_load: false });
  return banner.innerHTML.includes('status-change');
}

if (render(true)) throw new Error('状态页首次加载不应播放状态变化动效');
if (render(true)) throw new Error('状态未变化时不应重复播放动效');
if (!render(false)) throw new Error('状态由正常变为异常时没有强调变化');
if (render(false)) throw new Error('异常状态未变化时重复播放动效');
if (!render(true)) throw new Error('状态恢复时没有强调变化');

console.log('shared web motion regression checks passed');
