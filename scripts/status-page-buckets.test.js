const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'index.html'), 'utf8');

// 提取 buildBarEvents 和 renderBarsHtml 两个纯函数。
const sourceMatch = html.match(/function buildBarEvents\([\s\S]*?\n}\n\nfunction renderBarsHtml\([\s\S]*?\n}/);
if (!sourceMatch) throw new Error('未找到 buildBarEvents/renderBarsHtml');
const context = { Math };
vm.createContext(context);
vm.runInContext(sourceMatch[0], context);

const { buildBarEvents, renderBarsHtml } = context;
const historyLen = 5;

// 1. 样本不足 history_len 时左侧用透明占位，不产生 empty/paused 灰格。
const partial = renderBarsHtml({ history: [{ ts: 100, ok: true }] }, historyLen, 1000);
const partialNone = (partial.match(/bar none/g) || []).length;
const partialEmpty = (partial.match(/bar empty/g) || []).length;
const partialPaused = (partial.match(/bar paused/g) || []).length;
if (partialNone !== 4 || partialEmpty !== 0 || partialPaused !== 0) {
  throw new Error(`样本不足时应只有透明占位: none=${partialNone} empty=${partialEmpty} paused=${partialPaused}`);
}

// 2. 暂停区间在样本之间渲染为 paused 格。
const withPause = renderBarsHtml({
  history: [{ ts: 100, ok: true }, { ts: 300, ok: true }],
  pauses: [{ from: 150, to: 250 }],
}, historyLen, 1000);
const pauseCount = (withPause.match(/bar paused/g) || []).length;
if (pauseCount !== 1) {
  throw new Error(`应渲染 1 个暂停格，got ${pauseCount}`);
}
// 暂停格应在两个样本之间（按时间顺序排列）。
const barTypes = [...withPause.matchAll(/bar (ok|bad|paused|none|empty)"/g)].map(m => m[1]);
// 不足 5 个事件，应有透明占位在左侧。
if (!barTypes.includes('none')) {
  throw new Error('样本+暂停不足 history_len 应有透明占位');
}

// 3. 无暂停且样本不足时不应产生 empty 或 paused 灰格。
const noPauseShort = renderBarsHtml({
  history: [{ ts: 1, ok: true }, { ts: 2, ok: false }],
  pauses: [],
}, historyLen, 1000);
if (/bar (empty|paused)/.test(noPauseShort)) {
  throw new Error('无暂停时不应出现 empty 或 paused 格');
}

// 4. 样本数超过 history_len 时只保留最近 history_len 个事件。
const overflow = renderBarsHtml({
  history: Array.from({ length: 10 }, (_, i) => ({ ts: i + 1, ok: true })),
  pauses: [],
}, historyLen, 1000);
const overflowBars = (overflow.match(/bar ok/g) || []).length;
const overflowNone = (overflow.match(/bar none/g) || []).length;
if (overflowBars !== historyLen || overflowNone !== 0) {
  throw new Error(`超出 history_len 应只保留最近 ${historyLen} 个: ok=${overflowBars} none=${overflowNone}`);
}

// 5. buildBarEvents 按时间排序，暂停事件以 to 为排序键。
const events = buildBarEvents(
  [{ ts: 100, ok: true }, { ts: 300, ok: false }],
  [{ from: 150, to: 250 }],
);
if (events.length !== 3) {
  throw new Error(`应有 3 个事件，got ${events.length}`);
}
if (events[0].ts !== 100 || events[1].ts !== 250 || events[2].ts !== 300) {
  throw new Error(`事件应按时间排序: ${JSON.stringify(events)}`);
}
if (events[1].kind !== 'paused') {
  throw new Error(`中间事件应为 paused: ${events[1].kind}`);
}

console.log('status page bar rendering regression checks passed');
