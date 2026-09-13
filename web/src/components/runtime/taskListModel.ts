export type TaskTimeRange = 'all' | '24h' | '7d' | '30d' | 'before30d' | 'custom';
export type TaskTimeField = 'updated_at' | 'created_at';
export type TaskFilters = {
  status: 'all' | 'active' | 'blocked' | 'completed';
  query: string;
  timeField: TaskTimeField;
  timeRange: TaskTimeRange;
  fromDate: string;
  toDate: string;
};

const dayMilliseconds = 24 * 60 * 60 * 1000;

function localDateStart(value: string): Date {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) throw new Error('日期无效，请使用年-月-日格式。');
  const [year, month, day] = value.split('-').map(Number);
  const date = new Date(0);
  date.setFullYear(year, month - 1, day);
  date.setHours(0, 0, 0, 0);
  if (year < 1 || date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    throw new Error('日期无效，请选择真实存在的日期。');
  }
  return date;
}

export function buildTaskListQuery(filters: TaskFilters, offset: number, limit = 200, now = Date.now()): string {
  if (!Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(limit) || limit <= 0) {
    throw new Error('任务分页参数无效。');
  }
  const params = new URLSearchParams({
    status: filters.status,
    time_field: filters.timeField || 'updated_at',
    offset: String(offset),
    limit: String(limit),
  });
  const query = filters.query.trim();
  if (query) params.set('q', query);

  let from: Date | undefined;
  let to: Date | undefined;
  switch (filters.timeRange) {
    case 'all':
      break;
    case '24h':
    case '7d':
    case '30d': {
      const days = filters.timeRange === '24h' ? 1 : filters.timeRange === '7d' ? 7 : 30;
      from = new Date(now - days * dayMilliseconds);
      to = new Date(now + 1);
      break;
    }
    case 'before30d':
      to = new Date(now - 30 * dayMilliseconds);
      break;
    case 'custom': {
      if (!filters.fromDate && !filters.toDate) throw new Error('请至少选择一个开始日期或结束日期。');
      from = filters.fromDate ? localDateStart(filters.fromDate) : undefined;
      to = filters.toDate ? localDateStart(filters.toDate) : undefined;
      if (from && to && from > to) throw new Error('开始日期不能晚于结束日期。');
      // 结束日按本地日历加一天，夏令时切换日并不总是 24 小时。
      if (to) to.setDate(to.getDate() + 1);
      break;
    }
    default:
      throw new Error('任务时间范围无效。');
  }
  if ((from && !Number.isFinite(from.getTime())) || (to && !Number.isFinite(to.getTime()))) {
    throw new Error('任务筛选时间无效。');
  }
  if (from) params.set('from', from.toISOString());
  if (to) params.set('to', to.toISOString());
  return params.toString();
}

export async function deleteTaskBatch(
  ids: readonly string[],
  remove: (id: string) => Promise<void>,
  onProgress?: (completed: number, total: number) => void,
  signal?: AbortSignal,
): Promise<{ deleted: string[]; failed: { id: string; error: unknown }[] }> {
  // 删除期间即使调用者改选，也只处理确认时传入的 ID。
  const selectedIDs = Object.freeze([...new Set(ids)]);
  const outcomes = new Map<string, { ok: true } | { ok: false; error: unknown }>();
  let cursor = 0;
  let completed = 0;
  onProgress?.(0, selectedIDs.length);

  async function worker(): Promise<void> {
    while (!signal?.aborted && cursor < selectedIDs.length) {
      const id = selectedIDs[cursor++];
      try {
        await remove(id);
        outcomes.set(id, { ok: true });
      } catch (error) {
        outcomes.set(id, { ok: false, error });
      }
      completed++;
      onProgress?.(completed, selectedIDs.length);
    }
  }
  await Promise.all(Array.from({ length: Math.min(4, selectedIDs.length) }, () => worker()));
  const deleted: string[] = [];
  const failed: { id: string; error: unknown }[] = [];
  for (const id of selectedIDs) {
    const outcome = outcomes.get(id);
    if (outcome?.ok) deleted.push(id);
    else if (outcome) failed.push({ id, error: outcome.error });
  }
  return { deleted, failed };
}

export type TaskPage<T> = { items: T[]; total: number; offset: number; limit: number; has_more: boolean };

export async function collectTaskSelection<T extends { id: string }>(
  fetchPage: (offset: number) => Promise<TaskPage<T>>,
  onProgress?: (count: number, total: number) => void,
): Promise<T[]> {
  const selection: T[] = [];
  const seen = new Set<string>();
  let offset = 0;
  let total: number | undefined;
  for (;;) {
    const page = await fetchPage(offset);
    if (!page || !Array.isArray(page.items) || !Number.isSafeInteger(page.total) || page.total < 0
      || !Number.isSafeInteger(page.offset) || page.offset !== offset || !Number.isSafeInteger(page.limit)
      || page.limit <= 0 || typeof page.has_more !== 'boolean') {
      throw new Error('任务分页信息异常，请刷新列表后重新选择。');
    }
    if (total !== undefined && page.total !== total) throw new Error('任务总数已变化，请刷新列表后重新选择。');
    total = page.total;
    const nextOffset = offset + page.items.length;
    if (page.items.length !== Math.min(page.limit, total - offset)
      || page.has_more !== (nextOffset < total) || (page.has_more && nextOffset <= offset)) {
      throw new Error('任务分页数量异常，请刷新列表后重新选择。');
    }
    for (const item of page.items) {
      if (!item || typeof item.id !== 'string' || !item.id.trim() || seen.has(item.id)) {
        throw new Error('任务列表包含重复或无效任务，请刷新列表后重新选择。');
      }
      seen.add(item.id);
      selection.push(Object.freeze({ ...item }) as T);
    }
    onProgress?.(selection.length, total);
    if (!page.has_more) return Object.freeze(selection) as T[];
    offset = nextOffset;
  }
}
