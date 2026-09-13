export type ProjectNodeState = {
  id: string;
  enabled: boolean;
  online: boolean;
};

export type ProjectDeploymentState = {
  node_id: string;
  enabled: boolean;
  apply_status: string;
  desired_revision: string;
  applied_revision: string;
  last_error?: string;
};

export type UIStateLabel = { label: string; className: string; detail: string };

export function projectShellPermissionWarning(files: string, shell: boolean): string {
  if (!shell) return '';
  if (files === 'read_write') {
    return 'Shell 以目标 AgentDock 服务进程的 OS 账号权限运行；Git 也随 Shell 权限执行。';
  }
  const filesLabel = files === 'none' ? '禁用' : '只读';
  return `Shell 以目标 AgentDock 服务进程的 OS 账号权限运行；Files「${filesLabel}」只限制内置文件工具，Shell 仍可能写文件或通过 Git 修改工作树。`;
}

export function deploymentIsAvailable(deployment: ProjectDeploymentState, nodes: ProjectNodeState[]): boolean {
  const node = nodes.find((item) => item.id === deployment.node_id);
  return Boolean(
    deployment.enabled
      && deployment.apply_status === 'applied'
      && deployment.applied_revision
      && deployment.applied_revision === deployment.desired_revision
      && node?.enabled
      && node.online,
  );
}

export function deploymentApplyState(value: ProjectDeploymentState): UIStateLabel {
  if (!value.enabled || value.apply_status === 'disabled') {
    return { label: 'Node 已禁用此 Deployment', className: 'is-muted', detail: '配置保留在 Nexus；不提供新的执行 Target。' };
  }
  if (value.apply_status === 'failed') {
    return { label: 'Node 应用失败', className: 'is-danger', detail: value.last_error || '目标 Node 拒绝或无法应用 desired revision。' };
  }
  if (value.apply_status === 'pending' || !value.applied_revision || value.desired_revision !== value.applied_revision) {
    return { label: 'Nexus 已保存 · 等待 Node 应用', className: 'is-warning', detail: value.last_error || 'desired revision 已保存，尚未得到对应 applied revision。' };
  }
  if (value.apply_status === 'applied') {
    return { label: 'Node 已应用', className: 'is-ok', detail: 'desired/applied revision 一致。' };
  }
  return { label: value.apply_status || '未知状态', className: 'is-muted', detail: value.last_error || '等待状态刷新。' };
}

export function projectIDFromHash(hash: string): string {
  const raw = hash.replace(/^#/, '').split('?')[0] || '';
  const parts = raw.split('/').filter(Boolean);
  if (parts[0] !== 'projects' || !parts[1]) return '';
  try { return decodeURIComponent(parts[1]); } catch { return ''; }
}

export type PathBreadcrumb = { label: string; path: string };

export function projectPathBreadcrumbs(path: string, os?: string): PathBreadcrumb[] {
  if (!path) return [];
  if (os === 'windows') {
    const normalized = path.replace(/\//g, '\\');
    const drive = normalized.match(/^[A-Za-z]:\\?/u)?.[0];
    if (drive) {
      const root = `${drive.slice(0, 2)}\\`;
      const rest = normalized.slice(drive.length).split('\\').filter(Boolean);
      const result: PathBreadcrumb[] = [{ label: root, path: root }];
      let current = root.replace(/\\$/u, '');
      for (const segment of rest) {
        current = `${current}\\${segment}`;
        result.push({ label: segment, path: current });
      }
      return result;
    }
    return [{ label: normalized, path: normalized }];
  }
  const parts = path.split('/').filter(Boolean);
  const result: PathBreadcrumb[] = path.startsWith('/') ? [{ label: '/', path: '/' }] : [];
  let current = path.startsWith('/') ? '' : '.';
  for (const part of parts) {
    current = current === '' ? `/${part}` : current === '.' ? part : `${current}/${part}`;
    result.push({ label: part, path: current });
  }
  return result;
}

export function projectSessionStatusTone(status: string): string {
  if (['ready', 'idle', 'completed'].includes(status)) return 'is-ok';
  if (['running', 'preparing', 'partial'].includes(status)) return 'is-warning';
  if (['failed', 'cancelled', 'revoked', 'unavailable', 'context_error'].includes(status)) return 'is-danger';
  return 'is-muted';
}
