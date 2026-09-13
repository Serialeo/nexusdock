import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const modelPath = path.join(root, 'src/components/projects/projectUiModel.ts');
const source = fs.readFileSync(modelPath, 'utf8');
const output = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, strict: true },
  fileName: modelPath,
}).outputText;
const module = { exports: {} };
new Function('exports', 'module', output)(module.exports, module);
const model = module.exports;

const nodeOnline = [{ id: 'node-1', enabled: true, online: true }];
const applied = {
  node_id: 'node-1', enabled: true, apply_status: 'applied',
  desired_revision: 'rev-2', applied_revision: 'rev-2',
};

test('Deployment availability requires applied matching revision and live enabled Node', () => {
  assert.equal(model.deploymentIsAvailable(applied, nodeOnline), true);
  assert.equal(model.deploymentIsAvailable({ ...applied, apply_status: 'pending' }, nodeOnline), false);
  assert.equal(model.deploymentIsAvailable({ ...applied, applied_revision: 'rev-1' }, nodeOnline), false);
  assert.equal(model.deploymentIsAvailable({ ...applied, enabled: false }, nodeOnline), false);
  assert.equal(model.deploymentIsAvailable(applied, [{ id: 'node-1', enabled: true, online: false }]), false);
});

test('Deployment apply labels distinguish disabled, pending, failed and applied states', () => {
  assert.deepEqual(model.deploymentApplyState({ ...applied, enabled: false }), {
    label: 'Node 已禁用此 Deployment', className: 'is-muted', detail: '配置保留在 Nexus；不提供新的执行 Target。',
  });
  assert.equal(model.deploymentApplyState({ ...applied, apply_status: 'pending', applied_revision: 'rev-1' }).className, 'is-warning');
  assert.equal(model.deploymentApplyState({ ...applied, apply_status: 'failed', last_error: 'folder unavailable' }).detail, 'folder unavailable');
  assert.equal(model.deploymentApplyState(applied).className, 'is-ok');
});

test('Project hash routing decodes only the Projects detail route', () => {
  assert.equal(model.projectIDFromHash('#projects/project%20alpha'), 'project alpha');
  assert.equal(model.projectIDFromHash('#projects/project-1?tab=prompt'), 'project-1');
  assert.equal(model.projectIDFromHash('#recall'), '');
  assert.equal(model.projectIDFromHash('#projects/%E0%A4%A'), '');
});

test('Remote folder breadcrumbs preserve native POSIX and Windows path semantics', () => {
  assert.deepEqual(model.projectPathBreadcrumbs('/srv/project/backend', 'linux'), [
    { label: '/', path: '/' },
    { label: 'srv', path: '/srv' },
    { label: 'project', path: '/srv/project' },
    { label: 'backend', path: '/srv/project/backend' },
  ]);
  assert.deepEqual(model.projectPathBreadcrumbs('D:\\Dev\\Project', 'windows'), [
    { label: 'D:\\', path: 'D:\\' },
    { label: 'Dev', path: 'D:\\Dev' },
    { label: 'Project', path: 'D:\\Dev\\Project' },
  ]);
});

test('Shell permission warning does not misrepresent Files read-only/disabled as an OS sandbox', () => {
  assert.equal(model.projectShellPermissionWarning('read_only', false), '');
  assert.match(model.projectShellPermissionWarning('read_only', true), /Files「只读」只限制内置文件工具/);
  assert.match(model.projectShellPermissionWarning('none', true), /Files「禁用」只限制内置文件工具/);
  assert.match(model.projectShellPermissionWarning('read_write', true), /OS 账号权限运行/);
  assert.match(model.projectShellPermissionWarning('read_only', true), /Git 修改工作树/);
});

test('WorkSession and Target status tones keep failure/revocation distinct from ready/running', () => {
  assert.equal(model.projectSessionStatusTone('ready'), 'is-ok');
  assert.equal(model.projectSessionStatusTone('running'), 'is-warning');
  assert.equal(model.projectSessionStatusTone('context_error'), 'is-danger');
  assert.equal(model.projectSessionStatusTone('revoked'), 'is-danger');
  assert.equal(model.projectSessionStatusTone('future_status'), 'is-muted');
});
