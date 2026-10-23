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
import { ruleError } from '../../features/rules/api'

type TargetOption = { id: string; label: string }

type RuleEditorPageProps = {
  initialDraft: RuleEditorDraft
  instances: ReadonlyArray<TargetOption>
  groups: ReadonlyArray<TargetOption>
  sites?: ReadonlyArray<TargetOption>
  onSave?: (payload: RuleVersionPayload) => void | RuleEditorDraft | Promise<void | RuleEditorDraft>
  onCancel?: () => void
}

export function RuleEditorPage({ initialDraft, instances, groups, sites = [], onSave, onCancel }: RuleEditorPageProps) {
  const [draft, setDraft] = useState(initialDraft)
  const [tagsText, setTagsText] = useState(initialDraft.actions.tags.join(', '))
  const [errors, setErrors] = useState<ReturnType<typeof validateRuleDraft>>({})
  const [notice, setNotice] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const updateAction = <K extends keyof RuleActionDraft>(key: K, value: RuleActionDraft[K]) => {
    setDraft((current) => ({ ...current, actions: { ...current.actions, [key]: value } }))
    setErrors((current) => ({ ...current, target: undefined, participationWindowMinutes: undefined, limits: undefined }))
    setNotice(null)
    setSaveError(null)
  }

  const save = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!onSave || saving) return
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
      const saved = await onSave(payload)
      setDraft(saved ?? fromRuleVersionPayload(payload))
      setErrors({})
      setNotice(`规则已保存为 v${saved?.revision ?? payload.revision}。`)
    } catch (error) {
      setSaveError(`规则保存失败：${ruleError(error)}`)
    } finally {
      setSaving(false)
    }
  }

  const targetOptions = draft.actions.targetMode === 'FIXED_INSTANCE' ? instances : groups
  const targetValue = draft.actions.targetMode === 'FIXED_INSTANCE' ? draft.actions.targetInstanceId : draft.actions.targetGroupId

  return <section className="rule-editor-page" aria-labelledby="rule-editor-title">
    <header className="rule-editor-page-heading">
      <div>
        <p className="eyebrow">RULE EDITOR</p>
        <h1 id="rule-editor-title">{draft.id ? '编辑规则' : '新建规则'}</h1>
        <p className="muted">条件与动作保存到同一个不可变版本负载。</p>
      </div>
      <span className="page-badge">{draft.revision ? `v${draft.revision}` : '未保存'}</span>
    </header>

    {notice && <p className="sites-rss-notice" role="status">{notice}</p>}
    {saveError && <p className="rule-list-error" role="alert">{saveError}</p>}
    {!onSave && <p role="status">当前规则仅可查看，保存暂不可用。</p>}
    {instances.length === 0 && <p role="status">暂无可用下载器。请先完成下载器配置并等待同步，再选择投递目标。</p>}

    <form onSubmit={save} className="rule-editor-form" aria-label="规则编辑表单" noValidate>
      <section className="panel rule-editor-basics" aria-labelledby="rule-basics-title">
        <div className="rule-editor-section-heading"><div><p className="eyebrow">IDENTITY</p><h2 id="rule-basics-title">规则设置</h2></div><label className="checkbox-field"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} />启用规则</label></div>
        <div className="rule-editor-basics-grid">
          <label>适用站点<select aria-label="适用站点" disabled={Boolean(draft.id)} value={draft.siteId ?? ''} onChange={(event) => setDraft((current) => ({ ...current, siteId: event.target.value }))}><option value="">所有站点</option>{sites.map((site) => <option key={site.id} value={site.id}>{site.label}</option>)}</select></label>
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
            <button type="button" disabled={groups.length === 0} aria-pressed={draft.actions.targetMode === 'INSTANCE_GROUP'} onClick={() => updateAction('targetMode', 'INSTANCE_GROUP')}>qB 组</button>
          </div>
          <div className="rule-editor-action-fields">
            <label>{draft.actions.targetMode === 'FIXED_INSTANCE' ? '目标 qB' : '目标 qB 组'}<select aria-label="投递目标" value={targetValue} aria-invalid={Boolean(errors.target)} onChange={(event) => updateAction(draft.actions.targetMode === 'FIXED_INSTANCE' ? 'targetInstanceId' : 'targetGroupId', event.target.value)}><option value="">请选择</option>{targetOptions.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}</select></label>
            {errors.target && <p className="field-error" role="alert">{errors.target}</p>}
            <label>qB 分类<input value={draft.actions.category} onChange={(event) => updateAction('category', event.target.value)} placeholder="movie/4k" /></label>
            <label>Tags<span className="field-hint">逗号分隔，自动去重</span><input aria-label="Tags" value={tagsText} onChange={(event) => { setTagsText(event.target.value); setNotice(null) }} onBlur={() => updateAction('tags', parseTags(tagsText))} /></label>
            <label>保存路径策略<select aria-label="保存路径策略" value={draft.actions.savePathPolicy} onChange={(event) => updateAction('savePathPolicy', event.target.value as RuleActionDraft['savePathPolicy'])}><option value="INSTANCE_DEFAULT">使用 qB 默认路径</option><option value="CATEGORY_SUBDIRECTORY">按分类使用子目录</option></select></label>
            <label>参与窗口（分钟）<input type="number" min="1" max="1440" value={draft.actions.participationWindowMinutes} aria-invalid={Boolean(errors.participationWindowMinutes)} onChange={(event) => updateAction('participationWindowMinutes', Number(event.target.value))} /></label>
            {errors.participationWindowMinutes && <p className="field-error" role="alert">{errors.participationWindowMinutes}</p>}
            <label className="checkbox-field"><input type="checkbox" checked={draft.actions.startInactive ?? false} onChange={event => updateAction('startInactive', event.target.checked)} />添加后暂停（默认立即下载）</label>
            <label>上传限速（MB/s）<input type="number" min="0" step="0.001" value={(draft.actions.uploadLimitBytes ?? 0) / 1_000_000} onChange={event => updateAction('uploadLimitBytes', Math.round(Number(event.target.value) * 1_000_000))} /><span className="field-hint">1 MB/s = 1,000,000 bytes/s；0 不设置单种限速</span></label>
            <label>累计任务上限<input type="number" min="0" max="100000" value={draft.actions.maxJobs ?? 0} onChange={event => updateAction('maxJobs', Number(event.target.value))} /><span className="field-hint">0 不限；按本规则累计，重启或修改规则不会重置</span></label>
            <label>做种超过多少小时删除<input type="number" min="0" step="0.01" value={(draft.actions.seedTimeSeconds ?? 0) / 3600} onChange={event => updateAction('seedTimeSeconds', Math.round(Number(event.target.value) * 3600))} /><span className="field-hint">0 关闭；qui 每分钟检查，仅作用于本规则新任务</span></label>
            {(draft.actions.seedTimeSeconds ?? 0) > 0 && <label>到期删除方式<select value={draft.actions.deleteMode ?? ''} onChange={event => updateAction('deleteMode', event.target.value)}><option value="">请选择</option><option value="TORRENT_ONLY">仅删除任务，保留文件</option><option value="WITH_FILES">删除任务及下载文件</option></select></label>}
            {errors.limits && <p className="field-error" role="alert">{errors.limits}</p>}
            <details><summary>路径与兼容性限制</summary>
              <label>保存路径<input value={draft.actions.savePath ?? ''} onChange={event => updateAction('savePath', event.target.value)} placeholder="留空使用实例路径" /></label>
              <p className="field-hint">自定义路径或分类子目录须先在实例配置中提供对应磁盘验证凭据。</p>
              <label>允许的 Profile ID<input value={(draft.actions.allowedProfiles ?? []).join(',')} onChange={event => updateAction('allowedProfiles', parseTags(event.target.value))} placeholder="逗号分隔，留空不限" /></label>
              <label>所需存储标签<input value={(draft.actions.storageLabels ?? []).join(',')} onChange={event => updateAction('storageLabels', parseTags(event.target.value))} placeholder="逗号分隔，留空不限" /></label>
            </details>
            <label className="checkbox-field"><input type="checkbox" checked={draft.actions.sequentialDownload} onChange={(event) => updateAction('sequentialDownload', event.target.checked)} />顺序下载（白名单参数）</label>
          </div>
        </section>
      </div>

      <footer className="rule-editor-footer">
        <p>保存到服务器。启用后由 RSS 调度执行。停用规则也会停用其 qui 定时删除策略；修改策略仅用于新任务。</p>
        <div>{onCancel && <button type="button" className="ghost-button" onClick={onCancel}>返回规则</button>}<button type="submit" className="primary-button" disabled={saving || !onSave}>{saving ? '保存中…' : '保存规则'}</button></div>
      </footer>
    </form>
  </section>
}
