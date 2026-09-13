import { ApiError } from './api/client';

export function safeReturnTo(raw: string | null): string {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || /[\r\n]/.test(raw)) return '/ui/';
  try {
    const value = new URL(raw, window.location.origin);
    return value.origin === window.location.origin ? `${value.pathname}${value.search}${value.hash}` : '/ui/';
  } catch {
    return '/ui/';
  }
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'INVALID_CREDENTIALS') return '用户名或密码不正确。';
    if (error.code === 'LOGIN_RATE_LIMITED') return '尝试次数过多，请稍后再试。';
    if (error.code === 'ADMIN_NOT_INITIALIZED') return '管理员尚未初始化，请在 DockMini 本机执行初始化命令。';
    if (error.code === 'HTTPS_REQUIRED') return '登录仅允许通过 HTTPS 安全连接。';
    if (error.code === 'CURRENT_CREDENTIAL_INVALID') return '当前密码不正确。';
    if (error.code === 'CREDENTIAL_POLICY_FAILED') return error.message;
    return error.message;
  }
  return error instanceof Error ? error.message : '请求失败，请稍后重试。';
}
