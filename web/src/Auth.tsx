import { formatTime } from './lib/time';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import {
  ArrowRight,
  CheckCircle2,
  Clock3,
  KeyRound,
  Laptop,
  LockKeyhole,
  LogOut,
  RefreshCw,
  ShieldCheck,
  Smartphone,
  Trash2,
} from 'lucide-react';
import { ApiError, api, setCSRFToken } from './api/client';
import { errorMessage, safeReturnTo } from './authShared';
import './auth.css';

export type WebSession = {
  id: string;
  user_id: string;
  username: string;
  display_name: string;
  remember_me: boolean;
  ip_prefix: string;
  user_agent_summary: string;
  created_at: string;
  last_seen_at: string;
  idle_expires_at: string;
  absolute_expires_at: string;
  must_change_password: boolean;
  csrf_token?: string;
  current?: boolean;
};

type SessionResponse = { ok: boolean; session: WebSession };
type SessionsResponse = { ok: boolean; items: WebSession[] };

export function LoginPage() {
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const returnTo = safeReturnTo(params.get('return_to'));
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [rememberMe, setRememberMe] = useState(false);
  const [initialized, setInitialized] = useState<boolean | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await api<{ ok: boolean; initialized: boolean }>('/v1/auth/status');
        if (!cancelled) setInitialized(status.initialized);
        if (!status.initialized) return;
        try {
          const current = await api<SessionResponse>('/v1/auth/session');
          if (current.session.csrf_token) setCSRFToken(current.session.csrf_token);
          window.location.replace(current.session.must_change_password
            ? `/change-password?return_to=${encodeURIComponent(returnTo)}`
            : returnTo);
        } catch (sessionError) {
          if (!(sessionError instanceof ApiError) || sessionError.status !== 401) throw sessionError;
        }
      } catch (loadError) {
        if (!cancelled) setError(errorMessage(loadError));
      }
    })();
    return () => { cancelled = true; };
  }, [returnTo]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!initialized || submitting) return;
    setSubmitting(true);
    setError('');
    try {
      const result = await api<SessionResponse>('/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password, remember_me: rememberMe }),
      });
      if (result.session.csrf_token) setCSRFToken(result.session.csrf_token);
      setPassword('');
      window.location.assign(result.session.must_change_password
        ? `/change-password?return_to=${encodeURIComponent(returnTo)}`
        : returnTo);
    } catch (submitError) {
      setError(errorMessage(submitError));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="auth-shell">
      <section className="auth-login" aria-label="NexusDock">
        <div className="auth-brand"><span aria-hidden="true">N</span><strong>NexusDock</strong></div>
        <form className="auth-card" aria-labelledby="login-title" onSubmit={submit}>
          <header><span className="auth-card-icon"><LockKeyhole size={21} /></span><div><h2 id="login-title">登录控制台</h2><p>使用 NexusDock 管理员账号继续</p></div></header>
          {params.get('changed') === '1' && <div className="auth-success" role="status"><CheckCircle2 size={17} />密码已更新，请重新登录。</div>}
          {initialized === false && <div className="auth-error" role="alert">管理员尚未初始化。请在 DockMini 本机运行管理命令后刷新。</div>}
          {error && <div id="login-error" className="auth-error" role="alert">{error}</div>}
          <label htmlFor="login-username"><span>用户名</span><input id="login-username" name="username" type="text" autoComplete="username" autoCapitalize="none" spellCheck={false} aria-invalid={Boolean(error)} aria-describedby={error ? 'login-error' : undefined} value={username} onChange={(event) => setUsername(event.target.value)} disabled={submitting || initialized === false} required /></label>
          <label htmlFor="login-password"><span>密码</span><input id="login-password" name="password" type="password" autoComplete="current-password" aria-invalid={Boolean(error)} aria-describedby={error ? 'login-error' : undefined} value={password} onChange={(event) => setPassword(event.target.value)} disabled={submitting || initialized === false} required /></label>
          <label className="auth-check" htmlFor="login-remember"><input id="login-remember" name="remember_me" type="checkbox" checked={rememberMe} onChange={(event) => setRememberMe(event.target.checked)} /><span>记住我 30 天</span></label>
          <button className="auth-primary" type="submit" aria-busy={submitting} disabled={submitting || initialized !== true || !username || !password}>
            {submitting ? <><RefreshCw className="spin" size={17} />正在验证</> : <>进入 Nexus<ArrowRight size={17} /></>}
          </button>
          <p id="login-help" className="auth-help">忘记密码时，请在 DockMini 本机使用管理员恢复命令。Nexus 不提供公网找回入口。</p>
        </form>
        <div className="auth-security-note"><ShieldCheck size={15} /><span>HttpOnly Session · SameSite Strict · CSRF 防护</span></div>
      </section>
    </main>
  );
}
