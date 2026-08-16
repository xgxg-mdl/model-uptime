const fs = require('node:fs');
const path = require('node:path');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');

const requiredMarkup = [
  'class="service-table-wrap"',
  'class="service-table"',
  'class="service-primary" data-label="name"',
  'data-label="protocol"',
  'data-label="model"',
  'data-label="provider"',
  'data-label="interval"',
  'data-label="enabled"',
  'class="service-actions"',
];

for (const marker of requiredMarkup) {
  if (!html.includes(marker)) throw new Error(`missing responsive service marker: ${marker}`);
}

const requiredStyles = [
  '@media (max-width: 760px)',
  '.service-table thead { display: none; }',
  'content: attr(data-label)',
  '.service-table tbody .service-actions .actions',
  '.subscription-row { grid-template-columns: 1fr;',
];

for (const marker of requiredStyles) {
  if (!html.includes(marker)) throw new Error(`missing responsive admin style: ${marker}`);
}

console.log('admin responsive layout regression check passed');
