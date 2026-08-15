const fs = require('fs');
const path = require('path');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');

const requiredMarkup = [
  'id="update-current"',
  'id="update-latest"',
  'id="update-status"',
  'id="update-detail"',
  'id="update-check-btn"',
  'id="update-start-btn"',
];

for (const marker of requiredMarkup) {
  if (!html.includes(marker)) throw new Error(`missing update UI marker: ${marker}`);
}

const requiredBehavior = [
  "api('/api/admin/update')",
  "'/api/admin/update/check'",
  "api('/api/admin/update', { method: 'POST' })",
  "sessionStorage.setItem(UPDATE_TARGET_KEY",
  "data.current_version === target",
  "renderUpdateError('Waiting for the updated container to become available.'",
];

for (const marker of requiredBehavior) {
  if (!html.includes(marker)) throw new Error(`missing update UI behavior: ${marker}`);
}

console.log('admin update UI regression check passed');
