import { useEffect, useState } from 'react';
import { Clock3, KeyRound, Laptop, LogOut, RefreshCw, ShieldCheck, Smartphone, Trash2 } from 'lucide-react';
import { api, clearCSRFToken, setCSRFToken } from './api/client';
import { formatTime } from './lib/time';
import { type WebSession } from './Auth';
import { errorMessage } from './authShared';

type SessionResponse = { ok: boolean; session: WebSession };
type SessionsResponse = { ok: boolean; items: WebSession[] };

export default function AccountSecurity() {
  const [session, setSession] = useState<WebSession | null>(null);
  const [sessions, setSessions] = useState<WebSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [actionBusy, setActionBusy] = useState('');
  const [error, setError] = useState('');

  async function load() {
    setLoading(true);
    setError('');
    try {
      const [current, list] = await Promise.all([
        api<SessionResponse>('/v1/auth/session'),
        api<SessionsResponse>('/v1/auth/sessions'),
      ]);
      setSession(current.session);
      setSessions(list.items || []);
      if (current.session.csrf_token) setCSRFToken(current.session.csrf_token);
    } catch (loadError) {
      setError(errorMessage(loadError));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  async function revoke(id: string) {
    setActionBusy(`revoke:${id}`);
    setError('');
    try {
      await api(`/v1/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
      await load();
    } catch (revokeError) {
      setError(errorMessage(revokeError));
    } finally {
      setActionBusy('');
    }
  }

  async function logoutOthers() {
    setActionBusy('logout-others');
    setError('');
    try {
      await api('/v1/auth/sessions/logout-others', { method: 'POST', body: '{}' });
      await load();
    } catch (logoutError) {
      setError(errorMessage(logoutError));
    } finally {
      setActionBusy('');
    }
  }

  async function logout() {
    setActionBusy('logout');
    setError('');
    try {
      await api('/v1/auth/logout', { method: 'POST', body: '{}' });
      clearCSRFToken();
      window.location.assign('/login');
    } catch (logoutError) {
      setError(errorMessage(logoutError));
      setActionBusy('');
    }
  }

  return (
    <section className="security-panel">
      <header className="security-head"><div><span className="security-icon"><ShieldCheck size={19} /></span><div><h2>账号与会话</h2><p>管理当前管理员会话与登录客户端</p></div></div><button type="button" className="security-secondary" onClick={() => void load()} disabled={loading || Boolean(actionBusy)}><RefreshCw className={loading ? 'spin' : ''} size={15} />刷新</button></header>
      {error && <div className="auth-error">{error}</div>}
      <div className="security-profile"><div><strong>{session?.display_name || session?.username || 'Administrator'}</strong><span>{session?.username || '—'}</span></div><div className="security-actions"><button type="button" onClick={() => window.location.assign('/change-password?return_to=%2Fui%2F%23settings%2Faccount')} disabled={Boolean(actionBusy)}><KeyRound size={15} />修改密码</button><button type="button" onClick={() => void logout()} disabled={Boolean(actionBusy)}><LogOut size={15} />{actionBusy === 'logout' ? '正在退出…' : '退出登录'}</button></div></div>
      <div className="session-toolbar"><div><strong>活动会话</strong><span>{sessions.length} 个</span></div><button type="button" onClick={() => void logoutOthers()} disabled={Boolean(actionBusy) || sessions.filter((item) => !item.current).length === 0}>{actionBusy === 'logout-others' ? '正在退出…' : '退出其他会话'}</button></div>
      <div className="session-list">
        {sessions.map((item) => (
          <article className={`session-row ${item.current ? 'is-current' : ''}`} key={item.id}>
            <span className="session-client">{/iOS|Android/.test(item.user_agent_summary) ? <Smartphone size={18} /> : <Laptop size={18} />}</span>
            <div className="session-copy"><div><strong>{item.user_agent_summary || '未知客户端'}</strong>{item.current && <em>当前</em>}</div><span>{item.ip_prefix || '未知网络'} · {item.remember_me ? '记住我' : '浏览器会话'}</span><small><Clock3 size={12} />最近活动 {formatTime(item.last_seen_at, { fallback: '未知' })}</small></div>
            {!item.current && <button type="button" className="session-revoke" title="撤销会话" aria-label={`撤销 ${item.user_agent_summary || '未知客户端'} 会话`} onClick={() => void revoke(item.id)} disabled={Boolean(actionBusy)}><Trash2 size={16} /></button>}
          </article>
        ))}
        {!loading && sessions.length === 0 && <p className="session-empty">没有可显示的活动会话。</p>}
      </div>
    </section>
  );
}


