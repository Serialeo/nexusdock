import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Activity, BrainCircuit, DatabaseZap, RefreshCw, RotateCcw, Save, SearchCheck } from 'lucide-react';
import { ApiError, api } from '../../api/client';

const MAX_STAGE3_PROMPT_BYTES = 64 * 1024;
const utf8Encoder = new TextEncoder();

function promptByteLength(value: string): number {
  return utf8Encoder.encode(value).length;
}

function promptCharacterLength(value: string): number {
  return Array.from(value).length;
}

type SecretForm = { value: string; clear: boolean };
type EmbeddingSettings = {
  enabled: boolean;
  endpoint: string;
  model: string;
  timeout_seconds: number;
  api_key_configured: boolean;
};
type Stage3Settings = {
  enabled: boolean;
  endpoint: string;
  model: string;
  timeout_seconds: number;
  interval_minutes: number;
  api_key_configured: boolean;
  configured: boolean;
  system_prompt: string;
  bundled_system_prompt: string;
  system_prompt_source: 'bundled_default' | 'custom';
  system_prompt_updated_at?: string;
  review_node_id: string;
};
type RuntimeAISettings = {
  embedding: EmbeddingSettings;
  stage3: Stage3Settings;
  persisted: boolean;
  revision: string;
  updated_at?: string;
};
type SettingsResponse = { ok: boolean; settings: RuntimeAISettings };
type ConnectionTestResult = {
  ok: boolean;
  target: 'stage3' | 'embedding';
  model?: string;
  message: string;
  latency_ms: number;
};
type EmbeddingStatus = {
  ok: boolean;
  enabled: boolean;
  configured: boolean;
  reachable?: boolean;
  model?: string;
  error?: string;
  reason?: string;
  index?: { count?: number; dimension?: number; updated_at?: string };
};

type ReviewNode = { id: string; name: string; enabled: boolean; online: boolean };
type ReviewNodeListResponse = { ok: boolean; nodes: ReviewNode[] };

type FormState = {
  embedding: EmbeddingSettings;
  stage3: Stage3Settings;
};

const emptySettings: RuntimeAISettings = {
  embedding: { enabled: false, endpoint: '', model: 'BAAI/bge-m3', timeout_seconds: 30, api_key_configured: false },
  stage3: {
    enabled: false, endpoint: '', model: '', timeout_seconds: 60, interval_minutes: 360,
    api_key_configured: false, configured: false, system_prompt: '', bundled_system_prompt: '', system_prompt_source: 'bundled_default', review_node_id: '',
  },
  persisted: false, revision: 'rev-0',
};

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  return error instanceof Error ? error.message : '请求失败';
}

function secretAction(secret: SecretForm) {
  if (secret.clear) return { action: 'clear' };
  if (secret.value.trim()) return { action: 'replace', value: secret.value.trim() };
  return { action: 'keep' };
}

export default function AISettingsPanel({ refreshToken }: { refreshToken: number }) {
  const [form, setForm] = useState<FormState>({ embedding: emptySettings.embedding, stage3: emptySettings.stage3 });
  const [embeddingSecret, setEmbeddingSecret] = useState<SecretForm>({ value: '', clear: false });
  const [stage3Secret, setStage3Secret] = useState<SecretForm>({ value: '', clear: false });
  const [savedStage3Prompt, setSavedStage3Prompt] = useState('');
  const [stage3PromptReset, setStage3PromptReset] = useState(false);
  const [embeddingStatus, setEmbeddingStatus] = useState<EmbeddingStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [reindexing, setReindexing] = useState(false);
  const [testingTarget, setTestingTarget] = useState<'stage3' | 'embedding' | null>(null);
  const [stage3Test, setStage3Test] = useState<ConnectionTestResult | null>(null);
  const [embeddingTest, setEmbeddingTest] = useState<ConnectionTestResult | null>(null);
  const [notice, setNotice] = useState<{ tone: 'success' | 'error' | 'info'; text: string } | null>(null);
  const [reviewNodes, setReviewNodes] = useState<ReviewNode[]>([]);
  const [settingsRevision, setSettingsRevision] = useState('rev-0');
  const loadRequestID = useRef(0);

  async function refreshEmbeddingStatus() {
    try {
      setEmbeddingStatus(await api<EmbeddingStatus>('/v1/embeddings/status', { timeoutMs: 35_000 }));
    } catch (error) {
      setEmbeddingStatus({ ok: false, enabled: form.embedding.enabled, configured: false, reachable: false, error: errorMessage(error) });
    }
  }

  async function load() {
    const requestID = ++loadRequestID.current;
    setLoading(true);
    try {
      const [settingsResult, nodeResult] = await Promise.all([
        api<SettingsResponse>('/v1/settings/ai'),
        api<ReviewNodeListResponse>('/v1/runtime/nodes'),
      ]);
      if (requestID !== loadRequestID.current) return;
      setForm({ embedding: settingsResult.settings.embedding, stage3: settingsResult.settings.stage3 });
      setSettingsRevision(settingsResult.settings.revision || 'rev-0');
      setReviewNodes(nodeResult.nodes || []);
      setSavedStage3Prompt(settingsResult.settings.stage3.system_prompt);
      setStage3PromptReset(false);
      setEmbeddingSecret({ value: '', clear: false });
      setStage3Secret({ value: '', clear: false });
      setStage3Test(null);
      setEmbeddingTest(null);
      setNotice(null);
      void refreshEmbeddingStatus();
    } catch (error) {
      if (requestID === loadRequestID.current) setNotice({ tone: 'error', text: errorMessage(error) });
    } finally {
      if (requestID === loadRequestID.current) setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    return () => { loadRequestID.current += 1; };
  }, [refreshToken]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setNotice(null);
    try {
      const result = await api<SettingsResponse>('/v1/settings/ai', {
        method: 'PUT',
        headers: settingsRevision ? { 'If-Match': settingsRevision } : undefined,
        body: JSON.stringify({
          expected_revision: settingsRevision,
          embedding: {
            enabled: form.embedding.enabled,
            endpoint: form.embedding.endpoint.trim(),
            model: form.embedding.model.trim(),
            timeout_seconds: form.embedding.timeout_seconds,
            api_key: secretAction(embeddingSecret),
          },
          stage3: {
            enabled: form.stage3.enabled,
            endpoint: form.stage3.endpoint.trim(),
            model: form.stage3.model.trim(),
            timeout_seconds: form.stage3.timeout_seconds,
            interval_minutes: form.stage3.interval_minutes,
            api_key: secretAction(stage3Secret),
            review_node_id: form.stage3.review_node_id,
            system_prompt: stage3PromptReset
              ? { action: 'reset' }
              : form.stage3.system_prompt !== savedStage3Prompt
                ? { action: 'replace', value: form.stage3.system_prompt }
                : { action: 'keep' },
          },
        }),
      });
      setForm({ embedding: result.settings.embedding, stage3: result.settings.stage3 });
      setSavedStage3Prompt(result.settings.stage3.system_prompt);
      setSettingsRevision(result.settings.revision || settingsRevision);
      setStage3PromptReset(false);
      setEmbeddingSecret({ value: '', clear: false });
      setStage3Secret({ value: '', clear: false });
      setStage3Test(null);
      setEmbeddingTest(null);
      setNotice({ tone: 'success', text: '配置已保存并应用，无需重启 Nexus。' });
      void refreshEmbeddingStatus();
    } catch (error) {
      setNotice({ tone: 'error', text: errorMessage(error) });
    } finally {
      setSaving(false);
    }
  }

  async function reindex() {
    setReindexing(true);
    setNotice(null);
    try {
      await api('/v1/embeddings/reindex', { method: 'POST', body: '{}' , timeoutMs: 120_000 });
      await api('/v1/workflow-templates/reindex', { method: 'POST', timeoutMs: 120_000 });
      setNotice({ tone: 'success', text: 'Recall 与 Workflow 向量索引已重建。' });
      void refreshEmbeddingStatus();
    } catch (error) {
      setNotice({ tone: 'error', text: errorMessage(error) });
    } finally {
      setReindexing(false);
    }
  }

  async function testConnection(target: 'stage3' | 'embedding') {
    setTestingTarget(target);
    const setResult = target === 'stage3' ? setStage3Test : setEmbeddingTest;
    setResult(null);
    try {
      const result = await api<ConnectionTestResult>(`/v1/settings/ai/test/${target}`, {
        method: 'POST',
        timeoutMs: 310_000,
      });
      setResult(result);
    } catch (error) {
      setResult({ ok: false, target, message: errorMessage(error), latency_ms: 0 });
    } finally {
      setTestingTarget(null);
    }
  }

  const reachableTone = embeddingStatus?.reachable === true ? 'is-ok' : embeddingStatus?.enabled ? 'is-warn' : 'is-muted';
  const stage3State = form.stage3.enabled ? (form.stage3.configured ? '已启用' : '等待完整配置') : '已关闭';
  const embeddingState = embeddingStatus?.enabled ? (embeddingStatus.reachable ? '服务可用' : '服务不可达') : '已关闭';
  const embeddingMeta = embeddingStatus?.enabled
    ? (embeddingStatus.index ? `索引 ${embeddingStatus.index.count ?? 0} 项 · ${embeddingStatus.index.dimension ?? 0} 维` : '等待索引状态')
    : 'Recall 与 Workflow 暂不使用向量检索';
  const stage3PromptBytes = promptByteLength(form.stage3.system_prompt);
  const stage3PromptCharacters = promptCharacterLength(form.stage3.system_prompt);
  const stage3PromptTooLarge = stage3PromptBytes > MAX_STAGE3_PROMPT_BYTES;
  const stage3PromptEmpty = form.stage3.system_prompt.trim() === '';
  const stage3PromptChanged = form.stage3.system_prompt !== savedStage3Prompt;
  const stage3PromptSource = stage3PromptReset
    ? 'Bundled Default · 待保存'
    : stage3PromptChanged
      ? 'Custom · 待保存'
      : form.stage3.system_prompt_source === 'custom' ? 'Custom' : 'Bundled Default';
  const reviewNodeMissing = Boolean(form.stage3.review_node_id) && !reviewNodes.some((node) => node.id === form.stage3.review_node_id);

  return <section className="ai-settings-panel">
    <header className="ai-settings-heading">
      <div><span className="nexus-eyebrow">AI & VECTOR</span><h2>模型与向量检索</h2><p>管理 Stage 3 模型与共享 Embedding 服务。修改后统一保存并立即应用。</p></div>
      <button type="button" className="nx-button is-secondary" onClick={() => void load()} disabled={loading || saving}><RefreshCw size={15} />刷新</button>
    </header>
    {notice && <div className={`nx-alert is-${notice.tone}`}>{notice.text}</div>}

    <form className="ai-settings-form-page" onSubmit={submit}>
      <fieldset className="ai-settings-fields" disabled={loading || saving}>
      <section className="ai-config-section">
        <header className="ai-config-head">
          <div className="ai-config-title"><span className="nexus-panel-icon"><BrainCircuit size={17} /></span><div><h3>Stage 3 大模型</h3><p>跨 Task / Evolution 做低频语义补漏，只提交候选给 AgentDock 裁决。</p></div></div>
          <label className="ai-switch-row">
            <input type="checkbox" checked={form.stage3.enabled} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, enabled: event.target.checked } })} />
            <span><strong>{stage3State}</strong><small>{form.stage3.enabled ? '允许调用外部模型' : '不会调用外部模型'}</small></span>
          </label>
        </header>
        <div className="ai-config-body">
          <div className="ai-field-grid ai-stage3-fields">
            <label className="ai-field is-wide"><span>Chat Completions 地址</span><input type="url" required={form.stage3.enabled} value={form.stage3.endpoint} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, endpoint: event.target.value } })} placeholder="https://api.example.com/v1/chat/completions" /></label>
            <label className="ai-field"><span>模型</span><input required={form.stage3.enabled} value={form.stage3.model} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, model: event.target.value } })} placeholder="gpt-5-mini" /></label>
            <label className="ai-field"><span>请求超时（秒）</span><input type="number" min={1} max={300} value={form.stage3.timeout_seconds} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, timeout_seconds: Number(event.target.value) } })} /></label>
            <label className="ai-field"><span>执行间隔（分钟）</span><input type="number" min={60} max={10080} value={form.stage3.interval_minutes} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, interval_minutes: Number(event.target.value) } })} /></label>
            <label className="ai-field is-wide"><span>多节点 / 无唯一来源候选审阅节点</span><select value={form.stage3.review_node_id} onChange={(event) => setForm({ ...form, stage3: { ...form.stage3, review_node_id: event.target.value } })}><option value="">未配置 · 此类候选不派发</option>{reviewNodeMissing && <option value={form.stage3.review_node_id} disabled>已保存但节点不存在 · {form.stage3.review_node_id}</option>}{reviewNodes.map((node) => <option key={node.id} value={node.id} disabled={!node.enabled}>{node.name} · {node.id}{!node.enabled ? '（已停用）' : node.online ? '' : '（离线）'}</option>)}</select><small>只选择 Stage 3 proposal 的审阅执行节点，不改变 evidence 来源；已保存节点若停用/删除会保留显示但不会派发，也不会自动切到其他节点。</small></label>
            <label className="ai-field is-wide"><span>API Key {form.stage3.api_key_configured ? '· 已配置，留空保持' : '· 未配置'}</span><input type="password" autoComplete="new-password" disabled={stage3Secret.clear} value={stage3Secret.value} onChange={(event) => setStage3Secret({ value: event.target.value, clear: false })} placeholder={form.stage3.api_key_configured ? '••••••••' : '可选'} /></label>
            {form.stage3.api_key_configured && <label className="ai-clear-secret"><input type="checkbox" checked={stage3Secret.clear} onChange={(event) => setStage3Secret({ value: '', clear: event.target.checked })} /><span>清除已保存的 API Key</span></label>}
          </div>
          <section className="ai-prompt-editor">
            <header>
              <div><strong>Stage 3 System Prompt</strong><p>此文本会作为 Stage 3 请求的 system message。默认值来自仓库内 bundled resource，可显式覆盖或恢复。</p></div>
              <span className={`ai-prompt-source ${stage3PromptReset || (!stage3PromptChanged && form.stage3.system_prompt_source === 'bundled_default') ? 'is-bundled' : 'is-custom'}`}>Source: {stage3PromptSource}</span>
            </header>
            <textarea
              required
              rows={13}
              spellCheck={false}
              value={form.stage3.system_prompt}
              onChange={(event) => {
                setForm({ ...form, stage3: { ...form.stage3, system_prompt: event.target.value } });
                setStage3PromptReset(false);
              }}
              placeholder="Stage 3 System Prompt"
            />
            <div className="ai-prompt-meta">
              <span>{form.stage3.system_prompt_updated_at && form.stage3.system_prompt_source === 'custom' && !stage3PromptReset ? `Custom 最近保存 ${new Date(form.stage3.system_prompt_updated_at).toLocaleString()}` : 'Bundled Default 随 Nexus 版本交付；自定义覆盖存储在 SQLite。'}</span>
              <strong className={stage3PromptTooLarge ? 'is-over-limit' : ''}>{stage3PromptCharacters.toLocaleString()} 字符 · {stage3PromptBytes.toLocaleString()} / {MAX_STAGE3_PROMPT_BYTES.toLocaleString()} bytes</strong>
            </div>
            {(stage3PromptTooLarge || stage3PromptEmpty) && <p className="ai-prompt-error">{stage3PromptTooLarge ? 'UTF-8 内容超过 64 KiB，无法保存。' : 'System Prompt 不能为空。'}</p>}
            <footer>
              <span>恢复默认会删除 custom override，不会另存一份默认文本。</span>
              <button
                type="button"
                className="nx-button is-secondary"
                disabled={loading || saving || (!stage3PromptReset && form.stage3.system_prompt_source === 'bundled_default' && form.stage3.system_prompt === form.stage3.bundled_system_prompt)}
                onClick={() => {
                  setForm({ ...form, stage3: { ...form.stage3, system_prompt: form.stage3.bundled_system_prompt } });
                  setStage3PromptReset(form.stage3.system_prompt_source === 'custom');
                }}
              ><RotateCcw size={14} />恢复 Bundled Default</button>
            </footer>
          </section>
          <div className="ai-config-actions"><p>连接测试使用服务端当前已保存配置；修改表单后请先保存。</p><button type="button" className="nx-button is-secondary" disabled={loading || saving || testingTarget !== null} onClick={() => void testConnection('stage3')}><Activity size={15} />{testingTarget === 'stage3' ? '测试中…' : '测试连接'}</button></div>
          {stage3Test && <div className={`nx-alert is-${stage3Test.ok ? 'success' : 'error'}`}>{stage3Test.message}{stage3Test.latency_ms > 0 ? ` · ${stage3Test.latency_ms} ms` : ''}</div>}
        </div>
      </section>

      <section className="ai-config-section">
        <header className="ai-config-head">
          <div className="ai-config-title"><span className="nexus-panel-icon"><DatabaseZap size={17} /></span><div><h3>向量检索</h3><p>供 Recall 语义搜索与工作流模板匹配共用，兼容 OpenAI Embeddings API。</p></div></div>
          <div className="ai-config-head-actions">
            <div className="ai-service-status"><span className={`ai-status-dot ${reachableTone}`} /><span><strong>{embeddingState}</strong><small>{embeddingMeta}</small></span></div>
            <label className="ai-switch-row">
              <input type="checkbox" checked={form.embedding.enabled} onChange={(event) => setForm({ ...form, embedding: { ...form.embedding, enabled: event.target.checked } })} />
              <span><strong>{form.embedding.enabled ? '已启用' : '已关闭'}</strong><small>Recall 与 Workflow 共用</small></span>
            </label>
          </div>
        </header>
        <div className="ai-config-body">
          <div className="ai-field-grid ai-embedding-fields">
            <label className="ai-field is-wide"><span>Embeddings 地址</span><input type="url" required={form.embedding.enabled} value={form.embedding.endpoint} onChange={(event) => setForm({ ...form, embedding: { ...form.embedding, endpoint: event.target.value } })} placeholder="http://embedding-service:8000/v1/embeddings" /></label>
            <label className="ai-field"><span>Embedding 模型</span><input required={form.embedding.enabled} value={form.embedding.model} onChange={(event) => setForm({ ...form, embedding: { ...form.embedding, model: event.target.value } })} placeholder="BAAI/bge-m3" /></label>
            <label className="ai-field"><span>请求超时（秒）</span><input type="number" min={1} max={300} value={form.embedding.timeout_seconds} onChange={(event) => setForm({ ...form, embedding: { ...form.embedding, timeout_seconds: Number(event.target.value) } })} /></label>
            <label className="ai-field is-wide"><span>API Key {form.embedding.api_key_configured ? '· 已配置，留空保持' : '· 未配置'}</span><input type="password" autoComplete="new-password" disabled={embeddingSecret.clear} value={embeddingSecret.value} onChange={(event) => setEmbeddingSecret({ value: event.target.value, clear: false })} placeholder={form.embedding.api_key_configured ? '••••••••' : '本地 Embedding 可留空'} /></label>
            {form.embedding.api_key_configured && <label className="ai-clear-secret"><input type="checkbox" checked={embeddingSecret.clear} onChange={(event) => setEmbeddingSecret({ value: '', clear: event.target.checked })} /><span>清除已保存的 API Key</span></label>}
          </div>
          <div className="ai-config-actions"><p>重建索引会重新生成 Recall 与 Workflow 的向量数据。</p><div><button type="button" className="nx-button is-secondary" disabled={loading || saving || testingTarget !== null} onClick={() => void testConnection('embedding')}><Activity size={15} />{testingTarget === 'embedding' ? '测试中…' : '测试连接'}</button><button type="button" className="nx-button is-secondary" disabled={!form.embedding.enabled || reindexing || saving || testingTarget !== null} onClick={() => void reindex()}><SearchCheck size={15} />{reindexing ? '重建中…' : '重建索引'}</button></div></div>
          {embeddingTest && <div className={`nx-alert is-${embeddingTest.ok ? 'success' : 'error'}`}>{embeddingTest.message}{embeddingTest.latency_ms > 0 ? ` · ${embeddingTest.latency_ms} ms` : ''}</div>}
          {embeddingStatus?.error && <div className="nx-alert is-error">{embeddingStatus.error}</div>}
        </div>
      </section>
      </fieldset>

      <footer className="ai-save-bar"><span>API Key 只返回“已配置”状态；Stage 3 System Prompt 可见可审阅。设置版本：{settingsRevision}</span><button type="submit" className="nx-button" disabled={loading || saving || stage3PromptTooLarge || stage3PromptEmpty}><Save size={15} />{saving ? '保存中…' : '保存并应用'}</button></footer>
    </form>
  </section>;
}
