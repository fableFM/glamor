import { ArrowDown, ArrowUp, Copy, Trash2 } from 'lucide-react'
import type { SpecStage } from '../../lib/pipelineModel'
import { EFFORTS, GATE_AFTER_OPTIONS, HARNESSES, JANITOR_ON_FAIL } from '../../lib/pipelineModel'
import { PromptEditor } from './PromptEditor'

/*
  Инспектор этапа (T-20/T-22/T-28): ручки llm-stage или janitor формой
  (секции зависят от kind), членство в параллельной группе + read_only.
  Операции над нодой (вверх/вниз/дублировать/удалить) — в шапке панели.
  Ошибки валидации этой ноды показаны внизу.
*/

const inputCls =
  'w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-200 focus:border-violet-500 focus:outline-none disabled:opacity-50'
const labelCls = 'mb-1 block text-xs text-zinc-500'

export function StageInspector({
  stage,
  errors,
  readOnly,
  canMoveUp,
  canMoveDown,
  groupNames,
  onChange,
  onMoveUp,
  onMoveDown,
  onDuplicate,
  onDelete,
}: {
  stage: SpecStage
  errors: string[]
  readOnly: boolean
  canMoveUp: boolean
  canMoveDown: boolean
  /** имена объявленных параллельных групп (для select членства) */
  groupNames: string[]
  onChange: (stage: SpecStage) => void
  onMoveUp: () => void
  onMoveDown: () => void
  onDuplicate: () => void
  onDelete: () => void
}) {
  const set = <K extends keyof SpecStage>(key: K, value: SpecStage[K]) => onChange({ ...stage, [key]: value })

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-1 border-b border-zinc-800 px-3 py-2">
        <span className="font-mono text-sm font-medium text-zinc-200">{stage.key || '(без ключа)'}</span>
        <span className="rounded bg-zinc-800 px-1.5 text-xs text-zinc-500">{stage.kind}</span>
        <span className="ml-auto flex gap-0.5">
          <button type="button" title="выше" disabled={readOnly || !canMoveUp} onClick={onMoveUp}
            className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-30">
            <ArrowUp className="size-3.5" aria-hidden />
          </button>
          <button type="button" title="ниже" disabled={readOnly || !canMoveDown} onClick={onMoveDown}
            className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-30">
            <ArrowDown className="size-3.5" aria-hidden />
          </button>
          <button type="button" title="дублировать" disabled={readOnly} onClick={onDuplicate}
            className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-30">
            <Copy className="size-3.5" aria-hidden />
          </button>
          <button type="button" title="удалить" disabled={readOnly} onClick={onDelete}
            className="rounded p-1 text-zinc-500 hover:bg-red-500/20 hover:text-red-300 disabled:opacity-30">
            <Trash2 className="size-3.5" aria-hidden />
          </button>
        </span>
      </div>

      <div className="min-h-0 flex-1 space-y-3 overflow-auto p-3">
        <label className="block">
          <span className={labelCls}>key *</span>
          <input value={stage.key} disabled={readOnly} onChange={(e) => set('key', e.target.value)} className={`${inputCls} font-mono`} />
        </label>

        {stage.kind === 'llm-stage' && (
          <div className="grid grid-cols-3 gap-2">
            <label className="block">
              <span className={labelCls}>harness</span>
              <select value={stage.harness} disabled={readOnly} onChange={(e) => set('harness', e.target.value as SpecStage['harness'])} className={inputCls}>
                {HARNESSES.map((h) => (
                  <option key={h} value={h}>{h}</option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className={labelCls}>model</span>
              <input value={stage.model} disabled={readOnly} onChange={(e) => set('model', e.target.value)}
                placeholder="kimi-code/k3" className={`${inputCls} font-mono`} />
            </label>
            <label className="block">
              <span className={labelCls}>effort</span>
              <select value={stage.effort} disabled={readOnly} onChange={(e) => set('effort', e.target.value as SpecStage['effort'])} className={inputCls}>
                {EFFORTS.map((effort) => (
                  <option key={effort} value={effort}>{effort}</option>
                ))}
              </select>
            </label>
          </div>
        )}

        {stage.kind === 'janitor' && (
          <>
            <label className="block">
              <span className={labelCls}>commands (по строке на команду) *</span>
              <textarea
                value={stage.commands.join('\n')}
                disabled={readOnly}
                onChange={(e) => set('commands', e.target.value.split('\n'))}
                rows={4}
                spellCheck={false}
                placeholder={'make lint\nmake test'}
                className={`${inputCls} resize-y font-mono`}
              />
            </label>
            <div className="grid grid-cols-2 gap-2">
              <label className="block">
                <span className={labelCls}>on_fail</span>
                <select value={stage.on_fail} disabled={readOnly}
                  onChange={(e) => set('on_fail', e.target.value as SpecStage['on_fail'])} className={inputCls}>
                  {JANITOR_ON_FAIL.map((policy) => (
                    <option key={policy} value={policy}>{policy}</option>
                  ))}
                </select>
              </label>
              <label className="block">
                <span className={labelCls}>command_timeout_sec</span>
                <input type="number" min={1} value={stage.command_timeout_sec} disabled={readOnly}
                  onChange={(e) => set('command_timeout_sec', Number(e.target.value))} className={inputCls} />
              </label>
            </div>
          </>
        )}

        {/* параллельная группа (T-28): членство + обязательный read_only */}
        <div className="grid grid-cols-[1fr_auto] items-end gap-2 rounded-md border border-zinc-800 px-2 py-2">
          <label className="block">
            <span className={labelCls}>parallel_group</span>
            <select
              value={stage.parallel_group}
              disabled={readOnly || groupNames.length === 0}
              onChange={(e) => {
                // вход в группу → read_only принудительно (ADR-003), выход — сбрасываем
                const group = e.target.value
                onChange({ ...stage, parallel_group: group, read_only: group ? true : false })
              }}
              className={inputCls}
            >
              <option value="">— не в группе —</option>
              {groupNames.map((name) => (
                <option key={name} value={name}>{name}</option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1.5 pb-2 text-xs text-zinc-400" title="параллельные этапы обязаны быть read_only (ADR-003)">
            <input type="checkbox" checked={stage.read_only} disabled={readOnly || !stage.parallel_group}
              onChange={(e) => set('read_only', e.target.checked)} />
            read_only
          </label>
        </div>

        <div className="grid grid-cols-[1fr_auto] items-end gap-2">
          <label className="block">
            <span className={labelCls}>artifact.path</span>
            <input value={stage.artifact.path} disabled={readOnly}
              onChange={(e) => set('artifact', { ...stage.artifact, path: e.target.value })}
              placeholder="{run_dir}/spec.md" className={`${inputCls} font-mono`} />
          </label>
          <label className="flex items-center gap-1.5 pb-2 text-xs text-zinc-400">
            <input type="checkbox" checked={stage.artifact.required} disabled={readOnly}
              onChange={(e) => set('artifact', { ...stage.artifact, required: e.target.checked })} />
            required
          </label>
        </div>

        <label className="block">
          <span className={labelCls}>questions_path</span>
          <input value={stage.questions_path} disabled={readOnly} onChange={(e) => set('questions_path', e.target.value)}
            placeholder="{run_dir}/questions.md" className={`${inputCls} font-mono`} />
        </label>

        <label className="block">
          <span className={labelCls}>gate_after</span>
          <select value={stage.gate_after} disabled={readOnly} onChange={(e) => set('gate_after', e.target.value as SpecStage['gate_after'])} className={inputCls}>
            {GATE_AFTER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>{option.label}</option>
            ))}
          </select>
        </label>

        {stage.kind === 'llm-stage' && (
          <div>
            <span className={labelCls}>prompt_template</span>
            <PromptEditor value={stage.prompt_template} readOnly={readOnly} onChange={(v) => set('prompt_template', v)} />
          </div>
        )}

        {errors.length > 0 && (
          <ul className="space-y-1 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 py-2">
            {errors.map((message) => (
              <li key={message} className="text-xs text-red-300">{message}</li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
