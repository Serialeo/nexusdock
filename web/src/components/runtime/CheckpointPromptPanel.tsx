import { useEffect, useMemo, useState } from 'react';
import { RefreshCw, RotateCcw, Save, Undo2 } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import './CheckpointPromptPanel.css';

type CheckpointPromptResponse = {
  ok: boolean;
  settings: {
    prompt: string;
    default_prompt: string;
    source: 'bundled_default' | 'custom';
    revision: string;
    updated_at?: string;
    max_bytes: number;
  };
};

type Notice = { tone: 'success' | 'error' | 'info'; text: string };

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  return error instanceof Error ? error.message : '请求失败';
}

function isConflict(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 409 || error.code === 'REVISION_CONFLICT');
}

export default function CheckpointPromptPanel() {
  const [settings, setSettings] = useState<CheckpointPromptResponse['settings'] | null>(null);
  const [draft, setDraft] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [notice, setNotice] = useState<Notice | null>(null);
  const [resetPending, setResetPending] = useState(false);

  const bytes = useMemo(() => new TextEncoder().encode(draft).byteLength, [draft]);
  const maxBytes = settings?.max_bytes ?? 8192;
  const dirty = settings !== null && (draft !== settings.prompt || resetPending);
  const invalid = draft.trim().length === 0 || bytes > maxBytes;
  const busy = loading || saving;

  async function load(force = false) {
    if (!force && dirty) {
      setNotice({ tone: 'info', text: '当前有未保存编辑；请先撤销编辑后再刷新，避免覆盖输入。' });
      return;
    }
    setLoading(true);
    setLoadError('');
    setNotice(null);
    try {
      const result = await api<CheckpointPromptResponse>('/v1/settings/checkpoint');
      setSettings(result.settings);
      setDraft(result.settings.prompt);
      setResetPending(false);
    } catch (error) {
      setLoadError(errorMessage(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  function undo() {
    if (!settings) return;
    setDraft(settings.prompt);
    setResetPending(false);
    setNotice(null);
  }

  function restoreDefault() {
    if (!settings) return;
    setDraft(settings.default_prompt);
    setResetPending(settings.source === 'custom');
    setNotice(null);
  }

  async function save() {
    if (!settings || invalid || saving) return;
    setSaving(true);
    setNotice(null);
    try {
      const result = await api<CheckpointPromptResponse>('/v1/settings/checkpoint', {
        method: 'PUT',
        body: JSON.stringify(resetPending
          ? { expected_revision: settings.revision, action: 'reset' }
          : { expected_revision: settings.revision, action: 'replace', prompt: draft }),
      });
      setSettings(result.settings);
      setDraft(result.settings.prompt);
      setResetPending(false);
      setNotice({ tone: 'success', text: '已保存，AI 下次创建、读取或恢复任务时会收到最新 checkpoint 提示词。' });
    } catch (error) {
      setNotice({ tone: 'error', text: isConflict(error) ? 'Checkpoint 提示词已被其他操作更新；当前输入已保留，请显式重新加载后再决定是否保存。' : errorMessage(error) });
    } finally {
      setSaving(false);
    }
  }

  return <section className="checkpoint-prompt-panel">
    <header>
      <div>
        <h3>Checkpoint 提示词</h3>
      </div>
      <span className={`checkpoint-prompt-source ${settings?.source === 'custom' ? 'is-custom' : ''}`}>{settings?.source === 'custom' ? '自定义' : '默认'}</span>
    </header>

    {loadError && <div className="checkpoint-prompt-error nx-alert is-error" role="alert">{loadError}<button type="button" className="nx-button is-secondary is-small" onClick={() => void load(true)} disabled={busy}><RefreshCw size={13} />重试</button></div>}
    {notice && <div className={`checkpoint-prompt-notice nx-alert is-${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>{notice.text}{notice.tone === 'error' && notice.text.includes('显式重新加载') && <button type="button" className="nx-button is-secondary is-small" onClick={() => void load(true)} disabled={busy}><RefreshCw size={13} />重新加载</button>}</div>}

    {loading && !settings && <p className="checkpoint-prompt-body" role="status">正在读取提示词…</p>}
    {settings && !loadError && <div className="checkpoint-prompt-body">
      <fieldset disabled={busy}>
        <label className="checkpoint-prompt-field"><span>Checkpoint 提示词</span><textarea value={draft} onChange={(event) => { setDraft(event.target.value); setResetPending(false); }} spellCheck={false} aria-label="Checkpoint 提示词" rows={10} /></label>
      </fieldset>
      <div className="checkpoint-prompt-meta"><span>{bytes.toLocaleString()} / {maxBytes.toLocaleString()} bytes（UTF-8）</span><span>{settings?.revision ? `版本 ${settings.revision}` : ''}</span></div>
      {(draft.trim().length === 0 || bytes > maxBytes) && <p className="checkpoint-prompt-validation">{draft.trim().length === 0 ? '内容不能为空。' : `内容超过 ${maxBytes.toLocaleString()} bytes，无法保存。`}</p>}
      <div className="checkpoint-prompt-actions">
        <button type="button" className="nx-button is-secondary" onClick={undo} disabled={busy || !dirty}><Undo2 size={14} />撤销编辑</button>
        <button type="button" className="nx-button is-secondary" onClick={restoreDefault} disabled={busy || !settings || (settings.source === 'bundled_default' && draft === settings.default_prompt && !resetPending)}><RotateCcw size={14} />恢复默认</button>
        <button type="button" className="nx-button is-primary" onClick={() => void save()} disabled={busy || !dirty || invalid}><Save size={14} />{saving ? '保存中…' : '保存'}</button>
      </div>
    </div>}
  </section>;
}
