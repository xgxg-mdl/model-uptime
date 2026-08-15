const fs = require('fs');
const path = require('path');

const root = path.join(__dirname, '..');
const compose = fs.readFileSync(path.join(root, 'docker-compose.yml'), 'utf8');
const workflow = fs.readFileSync(path.join(root, '.github', 'workflows', 'docker-publish.yml'), 'utf8');
const dockerfile = fs.readFileSync(path.join(root, 'Dockerfile'), 'utf8');

const composeRequirements = [
  'nickfedor/watchtower:1.20.3',
  'WATCHTOWER_HTTP_API_ENDPOINTS: update,history',
  'image=ghcr.io/xgxg-mdl/model-uptime:latest&async=true',
  'com.centurylinklabs.watchtower.enable: "true"',
];
for (const marker of composeRequirements) {
  if (!compose.includes(marker)) throw new Error(`missing deployment guard: ${marker}`);
}

const appBlock = compose.split('model-uptime-updater:')[0];
if (appBlock.includes('/var/run/docker.sock')) throw new Error('the main app must not mount the Docker socket');
if (workflow.includes('workflow_dispatch:')) throw new Error('manual builds must not overwrite latest');
if (!workflow.includes('Only stable SemVer tags such as v1.2.3 may publish images.')) {
  throw new Error('release workflow does not reject prerelease or malformed tags');
}
if (!dockerfile.includes('-X main.version=${VERSION}')) throw new Error('build version is not embedded');

console.log('update deployment regression checks passed');
