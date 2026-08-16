import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));
const compose = fs.readFileSync(path.join(root, 'docker-compose.yml'), 'utf8');
const workflow = fs.readFileSync(path.join(root, '.github/workflows/docker-publish.yml'), 'utf8');
const dockerfile = fs.readFileSync(path.join(root, 'Dockerfile'), 'utf8');

test('更新部署保持最小权限和稳定发布约束', () => {
  for (const marker of [
    'nickfedor/watchtower:1.20.3',
    'WATCHTOWER_HTTP_API_ENDPOINTS: update,history',
    'image=ghcr.io/xgxg-mdl/model-uptime:latest&async=true',
    'com.centurylinklabs.watchtower.enable: "true"',
  ]) {
    assert.ok(compose.includes(marker), `missing deployment guard: ${marker}`);
  }

  const appBlock = compose.split('model-uptime-updater:')[0];
  assert.doesNotMatch(appBlock, /\/var\/run\/docker\.sock/);
  assert.doesNotMatch(workflow, /workflow_dispatch:/);
  assert.match(workflow, /Only stable SemVer tags such as v1\.2\.3 may publish images\./);
  assert.match(dockerfile, /-X main\.version=\$\{VERSION\}/);
  assert.match(dockerfile, /http:\/\/127\.0\.0\.1:8080\/healthz/);
  assert.doesNotMatch(dockerfile, /HEALTHCHECK[^]*\/api\/status/);
});
