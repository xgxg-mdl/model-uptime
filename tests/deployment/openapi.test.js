import assert from 'node:assert/strict';
import SwaggerParser from '@apidevtools/swagger-parser';
import fs from 'node:fs';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { parse } from 'yaml';

const contractPath = fileURLToPath(new URL('../../api/openapi.yaml', import.meta.url));
const serverPath = fileURLToPath(new URL('../../internal/httpserver/server.go', import.meta.url));
const contract = parse(fs.readFileSync(contractPath, 'utf8'));
const serverSource = fs.readFileSync(serverPath, 'utf8');
const operationMethods = new Set(['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace']);

function operations() {
  return Object.entries(contract.paths).flatMap(([path, pathItem]) =>
    Object.entries(pathItem)
      .filter(([method]) => operationMethods.has(method))
      .map(([method, operation]) => ({
        method: method.toUpperCase(),
        path,
        operation,
      })),
  );
}

test('OpenAPI contract matches every registered JSON API route', () => {
  const registered = [...serverSource.matchAll(/mux\.Handle(?:Func)?\("([A-Z]+) ([^"]+)"/g)]
    .map(([, method, path]) => `${method} ${path}`)
    .filter(route => route.includes(' /api/') || route.endsWith(' /healthz'))
    .sort();
  const documented = operations()
    .map(({ method, path }) => `${method} ${path}`)
    .sort();

  assert.deepEqual(documented, registered);
});

test('OpenAPI operations have stable identifiers and explicit authentication', () => {
  assert.equal(contract.openapi, '3.1.0');
  assert.deepEqual(contract.security, [{ bearerAuth: [] }]);
  assert.equal(contract.components.securitySchemes.bearerAuth.scheme, 'bearer');

  const publicRoutes = new Set([
    'GET /healthz',
    'GET /api/page',
    'GET /api/status',
    'GET /api/heatmap',
    'GET /api/admin/setup-status',
    'POST /api/admin/setup',
    'POST /api/admin/login',
  ]);
  const operationIDs = new Set();

  for (const { method, path, operation } of operations()) {
    const route = `${method} ${path}`;
    assert.ok(operation.operationId, `${route} is missing operationId`);
    assert.ok(!operationIDs.has(operation.operationId), `duplicate operationId: ${operation.operationId}`);
    operationIDs.add(operation.operationId);
    assert.ok(Object.keys(operation.responses || {}).length > 0, `${route} is missing responses`);

    if (publicRoutes.has(route)) {
      assert.deepEqual(operation.security, [], `${route} must be public`);
    } else {
      assert.equal(operation.security, undefined, `${route} must inherit bearer authentication`);
    }
  }
});

test('OpenAPI document passes schema and reference validation', async () => {
  const validated = await SwaggerParser.validate(contractPath);
  assert.equal(validated.openapi, '3.1.0');
});

test('OpenAPI preserves server-generated service identifiers', () => {
  const createSchema = contract.paths['/api/admin/services'].post.requestBody.content['application/json'].schema;
  const updateSchema = contract.paths['/api/admin/services/{id}'].put.requestBody.content['application/json'].schema;
  const responseSchema = contract.components.schemas.Service;

  assert.equal(createSchema.$ref, '#/components/schemas/ServiceInput');
  assert.equal(updateSchema.$ref, '#/components/schemas/ServiceInput');
  assert.ok(!contract.components.schemas.ServiceInput.required.includes('id'));
  assert.deepEqual(responseSchema.allOf.at(-1).required, ['id']);
});

test('OpenAPI distinguishes setup validation from login authentication', () => {
  const setupSchema = contract.paths['/api/admin/setup'].post.requestBody.content['application/json'].schema;
  const loginSchema = contract.paths['/api/admin/login'].post.requestBody.content['application/json'].schema;

  assert.equal(setupSchema.$ref, '#/components/schemas/SetupTokenRequest');
  assert.equal(loginSchema.$ref, '#/components/schemas/LoginTokenRequest');
  assert.equal(contract.components.schemas.SetupTokenRequest.properties.token.minLength, 8);
  assert.equal(contract.components.schemas.LoginTokenRequest.properties.token.minLength, 1);
});
