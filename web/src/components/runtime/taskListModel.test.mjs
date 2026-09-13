import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const modelPath = path.join(path.dirname(fileURLToPath(import.meta.url)), 'taskListModel.ts');
const output = ts.transpileModule(fs.readFileSync(modelPath, 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, strict: true },
  fileName: modelPath,
}).outputText;
const module = { exports: {} };
new Function('exports', 'module', output)(module.exports, module);
const { buildTaskListQuery, collectTaskSelection, deleteTaskBatch } = module.exports;
const defaults = {
  status: 'all', query: '', timeField: 'updated_at', timeRange: 'all', fromDate: '', toDate: '',
};
const query = (filters = {}, now = Date.UTC(2026, 8, 13, 12)) => new URLSearchParams(buildTaskListQuery({ ...defaults, ...filters }, 0, 200, now));
const nextTurn = () => new Promise((resolve) => setImmediate(resolve));

test('任务查询传递状态、时间字段、中文搜索和完整分页参数', () => {
  const params = new URLSearchParams(buildTaskListQuery({
    ...defaults, status: 'blocked', query: '  等待 A&B + 修复  ', timeField: 'created_at',
  }, 200, 75));
  assert.deepEqual(Object.fromEntries(params), {
    status: 'blocked', q: '等待 A&B + 修复', time_field: 'created_at', offset: '200', limit: '75',
  });
  const all = new URLSearchParams(buildTaskListQuery({ ...defaults, timeField: undefined, query: '  ' }, 0));
  assert.equal(all.get('time_field'), 'updated_at');
  assert.equal(all.get('limit'), '200');
  assert.equal(all.has('q'), false);
  assert.equal(all.has('from'), false);
  assert.equal(all.has('to'), false);
});

test('最近时间按滚动时长筛选，30 天前使用不含边界的上限', () => {
  const now = Date.UTC(2026, 8, 13, 12, 30, 15, 123);
  for (const [timeRange, days] of [['24h', 1], ['7d', 7], ['30d', 30]]) {
    const params = query({ timeRange }, now);
    assert.equal(Date.parse(params.get('from')), now - days * 86400000);
    assert.equal(Date.parse(params.get('to')), now + 1);
  }
  const old = query({ timeRange: 'before30d' }, now);
  assert.equal(old.has('from'), false);
  assert.equal(Date.parse(old.get('to')), now - 30 * 86400000);
});

test('自定义日期包含整个结束日，并按本地时区处理夏令时长短日', () => {
  const cases = [
    { day: '2026-03-08', from: '2026-03-08T08:00:00.000Z', to: '2026-03-09T07:00:00.000Z', hours: 23 },
    { day: '2026-11-01', from: '2026-11-01T07:00:00.000Z', to: '2026-11-02T08:00:00.000Z', hours: 25 },
    { day: '2026-09-13', from: '2026-09-13T07:00:00.000Z', to: '2026-09-14T07:00:00.000Z', hours: 24 },
  ];
  const previousTimezone = process.env.TZ;
  process.env.TZ = 'America/Los_Angeles';
  try {
    for (const expected of cases) {
      const actual = query({ timeRange: 'custom', fromDate: expected.day, toDate: expected.day });
      assert.equal(actual.get('from'), expected.from);
      assert.equal(actual.get('to'), expected.to);
      assert.equal((Date.parse(actual.get('to')) - Date.parse(actual.get('from'))) / 3600000, expected.hours);
    }
  } finally {
    if (previousTimezone === undefined) delete process.env.TZ;
    else process.env.TZ = previousTimezone;
  }
});

test('自定义日期支持单边和闰日，拒绝空日期、非法日期及倒序', () => {
  const fromOnly = query({ timeRange: 'custom', fromDate: '2024-02-29' });
  assert.equal(fromOnly.has('from'), true);
  assert.equal(fromOnly.has('to'), false);
  const toOnly = query({ timeRange: 'custom', toDate: '2026-09-13' });
  assert.equal(toOnly.has('from'), false);
  assert.equal(toOnly.has('to'), true);
  assert.throws(() => query({ timeRange: 'custom' }), /至少选择/);
  for (const date of ['2026-02-29', '2026-04-31', '2026-13-01', '2026-00-01', '2026-01-00', '2026-1-1', '0000-01-01', 'no-date']) {
    assert.throws(() => query({ timeRange: 'custom', fromDate: date }), /日期无效/, date);
    assert.throws(() => query({ timeRange: 'custom', toDate: date }), /日期无效/, date);
  }
  assert.throws(() => query({ timeRange: 'custom', fromDate: '2026-09-14', toDate: '2026-09-13' }), /开始日期不能晚于结束日期/);
  assert.throws(() => buildTaskListQuery(defaults, -1), /分页参数无效/);
});

test('全选枚举超过 200 条的全部分页并冻结独立确认快照', async () => {
  const items = Array.from({ length: 405 }, (_, index) => ({ id: `task-${index}`, title: `任务 ${index}` }));
  const originalIDs = items.map((item) => item.id);
  const offsets = [];
  const progress = [];
  const selected = await collectTaskSelection(async (offset) => {
    offsets.push(offset);
    if (offset === 200) items[0].id = 'changed-after-first-page';
    return { items: items.slice(offset, offset + 200), total: 405, offset, limit: 200, has_more: offset + 200 < 405 };
  }, (count, total) => progress.push([count, total]));
  assert.deepEqual(offsets, [0, 200, 400]);
  assert.deepEqual(progress, [[200, 405], [400, 405], [405, 405]]);
  assert.deepEqual(selected.map((item) => item.id), originalIDs);
  assert.equal(Object.isFrozen(selected), true);
  assert.equal(Object.isFrozen(selected[0]), true);
  assert.throws(() => selected.push({ id: 'new' }), TypeError);
  assert.throws(() => { selected[0].id = 'changed-after-confirmation'; }, TypeError);
});

test('空的筛选结果可以确认而无需继续分页', async () => {
  const progress = [];
  const selected = await collectTaskSelection(async (offset) => {
    assert.equal(offset, 0);
    return { items: [], total: 0, offset: 0, limit: 200, has_more: false };
  }, (count, total) => progress.push([count, total]));
  assert.deepEqual(selected, []);
  assert.deepEqual(progress, [[0, 0]]);
});

test('枚举中总数、重复 ID、分页偏移或数量变化都拒绝确认', async () => {
  const firstPage = { items: [{ id: 'a' }, { id: 'b' }], total: 4, offset: 0, limit: 2, has_more: true };
  const validSecond = { items: [{ id: 'c' }, { id: 'd' }], total: 4, offset: 2, limit: 2, has_more: false };
  const invalidPages = [
    { ...validSecond, total: 5 },
    { ...validSecond, items: [{ id: 'b' }, { id: 'd' }] },
    { ...validSecond, items: [{ id: 'c' }, { id: 'c' }] },
    { ...validSecond, offset: 0 },
    { ...validSecond, offset: 3 },
    { ...validSecond, items: [] },
    { ...validSecond, items: [{ id: 'c' }] },
    { ...validSecond, items: [{ id: 'c' }, { id: 'd' }, { id: 'e' }] },
    { ...validSecond, items: [{ id: '' }, { id: 'd' }] },
    { ...validSecond, has_more: true },
    { ...validSecond, limit: 0 },
  ];
  for (const second of invalidPages) {
    await assert.rejects(collectTaskSelection(async (offset) => offset === 0 ? firstPage : second), /请刷新列表后重新选择/);
  }
  await assert.rejects(collectTaskSelection(async () => ({ ...firstPage, has_more: false })), /请刷新列表后重新选择/);
  await assert.rejects(collectTaskSelection(async () => ({ ...firstPage, total: -1 })), /请刷新列表后重新选择/);
});

test('枚举网络失败直接返回错误，不能提供不完整删除范围', async () => {
  const error = new Error('目标节点离线');
  await assert.rejects(collectTaskSelection(async () => { throw error; }), (actual) => actual === error);
});

test('批量删除冻结去重 ID 并且最多四个请求并发', async () => {
  const ids = Array.from({ length: 11 }, (_, index) => `task-${index}`);
  const confirmed = [...ids];
  ids.push(ids[0]);
  let active = 0;
  let peak = 0;
  const called = [];
  const progress = [];
  const pending = deleteTaskBatch(ids, async (id) => {
    called.push(id);
    active++;
    peak = Math.max(peak, active);
    await nextTurn();
    active--;
  }, (completed, total) => progress.push([completed, total]));
  ids[0] = 'changed-after-confirmation';
  ids.push('newly-selected');
  const result = await pending;
  assert.equal(peak, 4);
  assert.equal(active, 0);
  assert.deepEqual(called, confirmed);
  assert.deepEqual(result, { deleted: confirmed, failed: [] });
  assert.deepEqual(progress, Array.from({ length: 12 }, (_, count) => [count, 11]));
});

test('单个删除失败不停止其他请求，保留原始错误且可只重试失败 ID', async () => {
  const offline = new Error('目标节点离线');
  const result = await deleteTaskBatch(['a', 'b', 'c', 'd', 'e'], async (id) => {
    if (id === 'b') throw offline;
    if (id === 'd') throw '权限拒绝';
  });
  assert.deepEqual(result, { deleted: ['a', 'c', 'e'], failed: [{ id: 'b', error: offline }, { id: 'd', error: '权限拒绝' }] });
  const retried = [];
  const retryResult = await deleteTaskBatch(result.failed.map((item) => item.id), async (id) => { retried.push(id); });
  assert.deepEqual(retried, ['b', 'd']);
  assert.deepEqual(retryResult, { deleted: ['b', 'd'], failed: [] });
});

test('取消删除后不派发新请求并等待已发出的请求返回部分结果', async () => {
  const controller = new AbortController();
  const calls = [];
  const gates = [];
  const progress = [];
  const failure = new Error('删除失败');
  const pending = deleteTaskBatch(['a', 'b', 'c', 'd', 'e', 'f'], (id) => {
    calls.push(id);
    return new Promise((resolve, reject) => gates.push({ resolve, reject }));
  }, (completed, total) => progress.push([completed, total]), controller.signal);
  assert.deepEqual(calls, ['a', 'b', 'c', 'd']);
  controller.abort();
  gates[0].resolve();
  gates[1].reject(failure);
  gates[2].resolve();
  gates[3].resolve();
  const result = await pending;
  assert.deepEqual(calls, ['a', 'b', 'c', 'd']);
  assert.deepEqual(result, { deleted: ['a', 'c', 'd'], failed: [{ id: 'b', error: failure }] });
  assert.deepEqual(progress, [[0, 6], [1, 6], [2, 6], [3, 6], [4, 6]]);
});

test('预先取消和空选择不会发出任何删除请求', async () => {
  const controller = new AbortController();
  controller.abort();
  const remove = async () => { assert.fail('不得发出删除请求'); };
  assert.deepEqual(await deleteTaskBatch(['a'], remove, undefined, controller.signal), { deleted: [], failed: [] });
  assert.deepEqual(await deleteTaskBatch([], remove), { deleted: [], failed: [] });
});
