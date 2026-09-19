import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import ts from 'typescript';

const output = ts.transpileModule(
  fs.readFileSync(new URL('./mcpTimeout.ts', import.meta.url), 'utf8'),
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } },
).outputText;
const module = { exports: {} };
new Function('exports', 'module', output)(module.exports, module);
const { mcpRefreshRequestTimeout } = module.exports;

test('refresh request covers configured MCP timeout plus bridge grace', () => {
  assert.equal(mcpRefreshRequestTimeout(30_000), 40_000);
  assert.equal(mcpRefreshRequestTimeout(300_000), 310_000);
  assert.equal(mcpRefreshRequestTimeout(600_000), 310_000);
  assert.equal(mcpRefreshRequestTimeout(undefined), 40_000);
});
