const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');

const requiredMarkup = [
  'class="service-toolbar"',
  'class="actions service-bulk-actions hidden" id="bulk-actions"',
  'class="service-table-wrap"',
  'class="service-table"',
  'class="service-list" id="svc-list"',
  'class="service-item" data-service-row',
  'class="service-item-meta"',
  'class="service-item-status"',
  'class="service-item-interval"',
  'class="service-item-actions"',
  'class="btn icon-btn${action.destructive',
  'title="${action.label}" aria-label="${action.label}"',
  'data.services.map(renderServiceTableRow)',
  'data.services.map(renderServiceListItem)',
  'Array.from(new Set(',
  'style="color:var(--dim);margin-top:10px;font-size:11px;"',
];

for (const marker of requiredMarkup) {
  if (!html.includes(marker)) throw new Error(`missing responsive service marker: ${marker}`);
}

const requiredStyles = [
  '@media (max-width: 959px)',
  '.service-table-wrap { display: none; }',
  'grid-template-columns: 20px minmax(0, 1fr) auto',
  '.service-item-actions .icon-btn { width: 32px; height: 32px;',
  'overflow: hidden; text-overflow: ellipsis; white-space: nowrap;',
  '@media (max-width: 559px)',
  '.update-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }',
  '.subscription-row { grid-template-columns: 1fr;',
  '.empty { color: var(--dim);',
];

for (const marker of requiredStyles) {
  if (!html.includes(marker)) throw new Error(`missing responsive admin style: ${marker}`);
}

for (const obsolete of ['@media (max-width: 960px)', 'content: attr(data-label)', 'data-label="protocol"', 'grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px']) {
  if (html.includes(obsolete)) throw new Error(`obsolete low-density service layout remains: ${obsolete}`);
}

const allowedColors = new Set([
  '#000', '#0d0d10', '#141418', '#15151a', '#1a1a1e', '#222228', '#24242a',
  '#26262d', '#2a2a30', '#3a3a42', '#55555e', '#5eff9c', '#7afcff', '#8a8a94',
  '#d4d4d4', '#ff5e7a', '#ffc857',
]);
const unexpectedColors = [...new Set(html.match(/#[0-9a-fA-F]{3,8}\b/g) || [])]
  .filter(color => !allowedColors.has(color.toLowerCase()));
if (unexpectedColors.length) throw new Error(`admin palette contains unreviewed colors: ${unexpectedColors.join(', ')}`);

const rendererStart = html.indexOf('function esc(s)');
const rendererEnd = html.indexOf('\nasync function loadServices', rendererStart);
if (rendererStart < 0 || rendererEnd < 0) throw new Error('service renderers not found');
const context = {};
vm.createContext(context);
vm.runInContext(html.slice(rendererStart, rendererEnd), context);

const service = {
  id: 'openai-main', name: 'Primary Model', protocol: 'chat', model: 'gpt-5',
  provider: 'OpenAI', interval_sec: 60, enabled: true,
};
const listItem = context.renderServiceListItem(service);
if (!listItem.includes('chat · gpt-5 · OpenAI') || !listItem.includes('service-item-interval">60s')) {
  throw new Error('mobile service summary does not keep compact metadata and interval');
}
if (!listItem.includes('title="Primary Model #openai-main"') || !listItem.includes('title="chat · gpt-5 · OpenAI"')) {
  throw new Error('truncated mobile service text does not expose its full value');
}
if ((listItem.match(/data-act=/g) || []).length !== 4 || (listItem.match(/class="btn icon-btn/g) || []).length !== 4) {
  throw new Error('mobile service actions are not a single four-button icon group');
}
for (const label of ['Edit service', 'Duplicate service', 'Test connection', 'Delete service']) {
  if (!listItem.includes(`title="${label}" aria-label="${label}"`)) {
    throw new Error(`service action lacks tooltip or accessible name: ${label}`);
  }
}
const tableRow = context.renderServiceTableRow(service);
if (!tableRow.startsWith('<tr data-service-row>') || !tableRow.includes('<td>chat</td>')) {
  throw new Error('desktop service table renderer is no longer intact');
}

let bulkUpdates = 0;
const primaryCheckbox = {
  dataset: { id: service.id }, checked: true,
  addEventListener(type, listener) { if (type === 'change') this.change = listener; },
};
const mirroredCheckbox = { dataset: { id: service.id }, checked: false };
context.document = {
  querySelectorAll(selector) {
    if (selector === '.row-check') return [primaryCheckbox, mirroredCheckbox];
    throw new Error(`unexpected document selector: ${selector}`);
  },
};
context.updateBulkBar = () => { bulkUpdates++; };
context.bindServiceControls({
  querySelectorAll(selector) {
    if (selector === 'button[data-act]') return [];
    if (selector === '.row-check') return [primaryCheckbox];
    throw new Error(`unexpected root selector: ${selector}`);
  },
}, [service]);
primaryCheckbox.change();
if (!mirroredCheckbox.checked || bulkUpdates !== 1) {
  throw new Error('desktop and mobile service selections do not stay synchronized');
}

const selectedStart = html.indexOf('function selectedIDs()');
const selectedEnd = html.indexOf('\nfunction updateBulkBar', selectedStart);
const selectionContext = {
  document: {
    querySelectorAll() {
      return [{ dataset: { id: 'a' } }, { dataset: { id: 'a' } }, { dataset: { id: 'b' } }];
    },
  },
};
vm.createContext(selectionContext);
vm.runInContext(html.slice(selectedStart, selectedEnd), selectionContext);
if (selectionContext.selectedIDs().join(',') !== 'a,b') {
  throw new Error('responsive duplicate checkboxes are not deduplicated for bulk actions');
}

function cssRule(selector, offset = 0) {
  const start = html.indexOf(`${selector} {`, offset);
  const end = html.indexOf('}', start);
  if (start < 0 || end < 0) throw new Error(`CSS rule not found: ${selector}`);
  return html.slice(start, end);
}

function cssNumber(rule, pattern, description) {
  const match = rule.match(pattern);
  if (!match) throw new Error(`CSS value not found: ${description}`);
  return Number(match[1]);
}

const mobileOffset = html.indexOf('@media (max-width: 559px)');
const bodyRule = cssRule('body');
const itemRule = cssRule('.service-item');
const metadataRule = cssRule('.service-item-meta');
const itemActionsRule = cssRule('.service-item-actions');
const iconRule = cssRule('.service-item-actions .icon-btn');
const actionsRule = cssRule('.service-item-actions .actions');
const mobileBodyRule = cssRule('body', mobileOffset);
const mobilePanelBodyRule = cssRule('.panel-body', mobileOffset);
const panelRule = cssRule('.panel');

const titleLineHeight = cssNumber(bodyRule, /font-size:\s*(\d+)px/, 'body font size')
  * cssNumber(bodyRule, /line-height:\s*([\d.]+)/, 'body line height');
const metadataLineHeight = cssNumber(metadataRule, /font-size:\s*(\d+)px/, 'metadata font size')
  * cssNumber(metadataRule, /line-height:\s*([\d.]+)/, 'metadata line height');
const verticalPadding = cssNumber(itemRule, /padding:\s*(\d+)px\s+0/, 'service row padding') * 2;
const gridGaps = cssNumber(itemRule, /row-gap:\s*(\d+)px/, 'service row gap') * 2;
const actionTopPadding = cssNumber(itemActionsRule, /padding-top:\s*(\d+)px/, 'action top padding');
const actionHeight = cssNumber(iconRule, /height:\s*(\d+)px/, 'action height');
const estimatedRowHeight = titleLineHeight + metadataLineHeight + verticalPadding + gridGaps + actionTopPadding + actionHeight;
if (estimatedRowHeight > 110) throw new Error(`mobile service row density regressed to ${estimatedRowHeight}px`);

const actionWidth = cssNumber(iconRule, /width:\s*(\d+)px/, 'action width');
const actionGap = cssNumber(actionsRule, /gap:\s*(\d+)px/, 'action gap');
const actionGroupWidth = (4 * actionWidth) + (3 * actionGap);
const bodyHorizontalPadding = cssNumber(mobileBodyRule, /padding:\s*\d+px\s+(\d+)px/, 'mobile body horizontal padding');
const panelBorder = cssNumber(panelRule, /border:\s*(\d+)px/, 'panel border');
const panelBodyPadding = cssNumber(mobilePanelBodyRule, /padding:\s*(\d+)px/, 'mobile panel padding');
const selectionColumn = cssNumber(itemRule, /grid-template-columns:\s*(\d+)px/, 'selection column');
const columnGap = cssNumber(itemRule, /column-gap:\s*(\d+)px/, 'service column gap');
for (const viewportWidth of [320, 390]) {
  const panelContentWidth = viewportWidth - (2 * bodyHorizontalPadding) - (2 * panelBorder) - (2 * panelBodyPadding);
  const actionAreaWidth = panelContentWidth - selectionColumn - columnGap;
  if (actionGroupWidth > actionAreaWidth) {
    throw new Error(`service actions overflow at ${viewportWidth}px`);
  }
}

console.log('admin responsive layout regression check passed');
