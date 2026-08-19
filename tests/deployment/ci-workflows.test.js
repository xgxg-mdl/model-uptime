import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));

for (const workflowName of ['ci.yml', 'docker-publish.yml']) {
  test(`${workflowName} 不会把 false 当作 setup-node 缓存类型`, () => {
    const workflow = fs.readFileSync(path.join(root, '.github/workflows', workflowName), 'utf8');
    const setupNodeStep = workflow.match(
      /- name: Set up Node\.js[\s\S]*?(?=\n\s{6}- name:|\n\s{2}[a-z][\w-]*:|$)/,
    )?.[0];

    assert.ok(setupNodeStep, `${workflowName} is missing the setup-node step`);
    assert.doesNotMatch(
      setupNodeStep,
      /^\s*cache:\s*false\s*$/m,
      `${workflowName} must omit setup-node's cache input when caching is disabled`,
    );
  });
}
