import { useState } from 'react'
import { VertexRuleEditor } from './VertexRuleEditor'
import {
  fromRuleVersionPayload,
  parseTags,
  toRuleVersionPayload,
  validateRuleDraft,
  type RuleActionDraft,
  type RuleEditorDraft,
  type RuleVersionPayload,
} from '../../features/rules/rule-editor'

type TargetOption = { id: string; label: string }

type RuleEditorPageProps = {
  initialDraft: RuleEditorDraft
  instances: ReadonlyArray<TargetOption>
  groups: ReadonlyArray<TargetOption>
  onSave?: (payload: RuleVersionPayload) => void | Promise<void>
  onCancel?: () => void
}

export function RuleEditorPage({ initialDraft, instances, groups, onSave, onCancel }: RuleEditorPageProps) {
  const [draft, setDraft] = useState(initialDraft)
  const [tagsText, setTagsText] = useState(initialDraft.actions.tags.join(', '))
  const [errors, setErrors] = useState<ReturnType<typeof validateRuleDraft>>({})
  const [notice, setNotice] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const updateAction = <K extends keyof RuleActionDraft>(key: K, value: RuleActionDraft[K]) => {
    setDraft((current) => ({ ...current, actions: { ...current.actions, [key]: value } }))
    setErrors((current) => ({ ...current, target: undefined, participationWindowMinutes: undefined }))
    setNotice(null)
    setSaveError(null)
  }

  const save = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const normalized = { ...draft, actions: { ...draft.actions, tags: parseTags(tagsText) } }
    const nextErrors = validateRuleDraft(normalized)
    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors)
      setNotice(null)
      return
    }
    const payload = toRuleVersionPayload(normalized)
    setSaving(true)
    setSaveError(null)
    try {
      await onSave?.(payload)
      setDraft(fromRuleVersionPayload(payload))
      setErrors({})
      setNotice(`规则草稿已保存为本地 v${payload.revision}。`)
    } catch (error) {
      setSaveError(`规则保存失败：${String(error)}`)
    } finally {
      setSaving(false)
    }
  }

  const targetOptions = draft.actions.targetMode === 'FIXED_INSTANCE' ? instances : groups
  const targetValue = draft.actions.targetMode === 'FIXED_INSTANCE' ? draft.actions.targetInstanceId : draft.actions.targetGroupId

  return <section className="rule-editor-page" aria-labelledby="rule-editor-title">
    <header className="rule-editor-page-heading">
      <div>
        <p className="eyebrow">RULE EDITOR · LOCAL VERSION</p>
        <h1 id="rule-editor-title">编辑规则</h1>
        <p className="muted">条件与动作保存到同一个不可变版本负载。</p>
      </div>
      <span className="page-badge">v{draft.revision} · LOCAL ONLY</span>
    </header>

    {notice && <p className="sites-rss-notice" role="status">{notice}</p>}
    {saveError && <p className="rule-list-error" role="alert">{saveError}</p>}

    <form onSubmit={save} className="rule-editor-form" aria-label="规则编辑表单" noValidate>
      <section className="panel rule-editor-basics" aria-labelledby="rule-basics-title">
        <div className="rule-editor-section-heading"><div><p className="eyebrow">IDENTITY</p><h2 id="rule-basics-title">规则设置</h2></div><label className="checkbox-field"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} />启用规则</label></div>
        <div className="rule-editor-basics-grid">
          <label>规则名称<input value={draft.name} aria-invalid={Boolean(errors.name)} onChange={(event) => { setDraft((current) => ({ ...current, name: event.target.value })); setErrors((current) => ({ ...current, name: undefined })) }} /></label>
          <label>优先级<input type="number" min="0" max="10000" value={draft.priority} aria-invalid={Boolean(errors.priority)} onChange={(event) => { setDraft((current) => ({ ...current, priority: Number(event.target.value) })); setErrors((current) => ({ ...current, priority: undefined })) }} /></label>
        </div>
        {errors.name && <p className="field-error" role="alert">{errors.name}</p>}
        {errors.priority && <p className="field-error" role="alert">{errors.priority}</p>}
      </section>

      <div className="rule-editor-workspace">
        <section className="panel rule-editor-conditions" aria-labelledby="rule-conditions-title">
          <div className="rule-editor-section-heading"><div><p className="eyebrow">WHEN</p><h2 id="rule-conditions-title">匹配条件</h2></div><span className="muted">共享 AST</span></div>
          <VertexRuleEditor value={draft.conditionTree} onChange={(conditionTree) => { setDraft((current) => ({ ...current, conditionTree })); setNotice(null) }} />
        </section>

        <section className="panel rule-editor-actions" aria-labelledby="rule-actions-title">
          <div className="rule-editor-section-heading"><div><p className="eyebrow">THEN</p><h2 id="rule-actions-title">投递动作</h2></div><span className="muted">受控参数</span></div>
          <div className="rule-target-mode" role="group" aria-label="目标模式">
            <button type="button" aria-pressed={draft.actions.targetMode === 'FIXED_INSTANCE'} onClick={() => updateAction('targetMode', 'FIXED_INSTANCE')}>固定 qB</button>
            <button type="button" aria-pressed={draft.actions.targetMode === 'INSTANCE_GROUP'} onClick={() => updateAction('targetMode', 'INSTANCE_GROUP')}>qB 组</button>
          </div>
          <div className="rule-editor-action-fields">
            <label>{draft.actions.targetMode === 'FIXED_INSTANCE' ? '目标 qB' : '目标 qB 组'}<select aria-label="投递目标" value={targetValue} aria-invalid={Boolean(errors.target)} onChange={(event) => updateAction(draft.actions.targetMode === 'FIXED_INSTANCE' ? 'targetInstanceId' : 'targetGroupId', event.target.value)}><option value="">请选择</option>{targetOptions.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}</select></label>
            {errors.target && <p className="field-error" role="alert">{errors.target}</p>}
            <label>qB 分类<input value={draft.actions.category} onChange={(event) => updateAction('category', event.target.value)} placeholder="movie/4k" /></label>
            <label>Tags<span className="field-hint">逗号分隔，自动去重</span><input aria-label="Tags" value={tagsText} onChange={(event) => { setTagsText(event.target.value); setNotice(null) }} onBlur={() => updateAction('tags', parseTags(tagsText))} /></label>
            <label>保存路径策略<select aria-label="保存路径策略" value={draft.actions.savePathPolicy} onChange={(event) => updateAction('savePathPolicy', event.target.value as RuleActionDraft['savePathPolicy'])}><option value="INSTANCE_DEFAULT">使用 qB 默认路径</option><option value="CATEGORY_SUBDIRECTORY">按分类使用子目录</option></select></label>
            <label>参与窗口（分钟）<input type="number" min="1" max="1440" value={draft.actions.participationWindowMinutes} aria-invalid={Boolean(errors.participationWindowMinutes)} onChange={(event) => updateAction('participationWindowMinutes', Number(event.target.value))} /></label>
            {errors.participationWindowMinutes && <p className="field-error" role="alert">{errors.participationWindowMinutes}</p>}
            <label className="checkbox-field"><input type="checkbox" checked={draft.actions.sequentialDownload} onChange={(event) => updateAction('sequentialDownload', event.target.checked)} />顺序下载（白名单参数）</label>
          </div>
        </section>
      </div>

      <footer className="rule-editor-footer">
        <p>保存只生成本地规则版本，不下载、不创建 Reservation、不发送 Add。</p>
        <div>{onCancel && <button type="button" className="ghost-button" onClick={onCancel}>返回规则</button>}<button type="submit" className="primary-button" disabled={saving}>{saving ? '保存中…' : '保存本地版本'}</button></div>
      </footer>
    </form>
  </section>
}
