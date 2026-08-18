import { useEffect, useMemo, useState } from 'react'
import {
  applyNodeChanges,
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeChange,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Flag } from 'lucide-react'
import type { PipelineSpec } from '../../lib/pipelineModel'

/*
  Канва редактора пайплайна (T-20, M1): этапы — вертикальная цепочка
  (позиции по индексу, drag разрешён, но позиции не сохраняются —
  источник истины это порядок в массиве stages), loop-ребро петлёй
  слева с подписью «i/max_iters», final_gate — финальная нода.
  Ноды с ошибками валидации подсвечены красным.
*/

type StageNodeData = {
  stageKey: string
  triplet: string
  gateAfter: string
  hasError: boolean
  selectedKey: boolean
  /** параллельная группа (T-28): имя + цвет рамки, пусто — не в группе */
  groupName: string
  groupColor: string
  kind: string
}
type StageFlowNode = Node<StageNodeData, 'stage'>
type FinalFlowNode = Node<Record<string, never>, 'finalGate'>
type FlowNode = StageFlowNode | FinalFlowNode

const NODE_X = 120
const NODE_DY = 130

/** палитра цветов групп по индексу объявления (T-28): члены одной группы — одним цветом */
const GROUP_COLORS = ['#38bdf8', '#f472b6', '#a3e635', '#fb923c', '#c084fc', '#34d399']

function groupColor(spec: PipelineSpec, name: string): string {
  const index = (spec.parallel_groups ?? []).findIndex((g) => g.name === name)
  return GROUP_COLORS[((index % GROUP_COLORS.length) + GROUP_COLORS.length) % GROUP_COLORS.length]
}

function StageNodeView({ data, selected }: NodeProps<StageFlowNode>) {
  return (
    <div
      className={`w-52 rounded-lg border px-3 py-2 ${
        data.hasError
          ? 'border-red-500/70 bg-red-500/10'
          : selected || data.selectedKey
            ? 'border-violet-500/70 bg-violet-500/10'
            : 'border-zinc-700 bg-zinc-900'
      }`}
      // член группы — цветная левая кромка (T-28), ошибка/выделение важнее
      style={data.groupName && !data.hasError && !selected && !data.selectedKey ? { borderLeftColor: data.groupColor, borderLeftWidth: 3 } : undefined}
    >
      <Handle type="target" position={Position.Top} className="!bg-zinc-600" />
      <Handle type="target" position={Position.Left} id="loop-in" className="!bg-amber-400" />
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium text-zinc-100">
          {data.stageKey || '(без ключа)'}
        </span>
        {data.kind === 'janitor' && (
          <span className="shrink-0 rounded bg-zinc-800 px-1 text-[10px] text-zinc-500">janitor</span>
        )}
      </div>
      {data.triplet && <div className="mt-0.5 truncate text-xs text-zinc-500">{data.triplet}</div>}
      {data.gateAfter && (
        <div className="mt-1 inline-block rounded bg-amber-500/15 px-1.5 text-[10px] text-amber-300">
          gate: {data.gateAfter}
        </div>
      )}
      {data.groupName && (
        <div
          className="ml-0 mt-1 inline-block rounded px-1.5 text-[10px]"
          style={{ backgroundColor: `${data.groupColor}22`, color: data.groupColor }}
        >
          ∥ {data.groupName}
        </div>
      )}
      <Handle type="source" position={Position.Bottom} className="!bg-zinc-600" />
      <Handle type="source" position={Position.Left} id="loop-out" className="!bg-amber-400" />
    </div>
  )
}

function FinalGateNodeView({ selected }: NodeProps<FinalFlowNode>) {
  return (
    <div
      className={`flex w-52 items-center gap-2 rounded-lg border px-3 py-2 ${
        selected ? 'border-violet-500/70 bg-violet-500/10' : 'border-emerald-700/60 bg-emerald-500/10'
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-zinc-600" />
      <Flag className="size-4 text-emerald-400" aria-hidden />
      <span className="text-sm font-medium text-emerald-200">финальный гейт</span>
    </div>
  )
}

const nodeTypes = { stage: StageNodeView, finalGate: FinalGateNodeView }

function stageNodeId(index: number): string {
  return `s${index}`
}

function buildNodes(spec: PipelineSpec, errorKeys: ReadonlySet<string>, selectedIndex: number | null, prev: FlowNode[]): FlowNode[] {
  const prevPos = new Map(prev.map((n) => [n.id, n.position]))
  const nodes: FlowNode[] = spec.stages.map((stage, index) => ({
    id: stageNodeId(index),
    type: 'stage' as const,
    position: prevPos.get(stageNodeId(index)) ?? { x: NODE_X, y: index * NODE_DY },
    data: {
      stageKey: stage.key,
      // у janitor триплет пуст — не показываем мусор «kimi · · high»
      triplet: stage.kind === 'janitor' ? '' : [stage.harness, stage.model, stage.effort].filter(Boolean).join(' · '),
      gateAfter: stage.gate_after,
      hasError: errorKeys.has(stage.key),
      selectedKey: index === selectedIndex,
      groupName: stage.parallel_group,
      groupColor: stage.parallel_group ? groupColor(spec, stage.parallel_group) : '',
      kind: stage.kind,
    },
  }))
  if (spec.final_gate) {
    nodes.push({
      id: 'final',
      type: 'finalGate' as const,
      position: prevPos.get('final') ?? { x: NODE_X, y: spec.stages.length * NODE_DY },
      data: {},
      draggable: false,
    })
  }
  return nodes
}

function buildEdges(spec: PipelineSpec): Edge[] {
  const edges: Edge[] = []
  for (let i = 0; i < spec.stages.length - 1; i++) {
    edges.push({
      id: `e${i}`,
      source: stageNodeId(i),
      target: stageNodeId(i + 1),
      style: { stroke: '#52525b' },
    })
  }
  const lastIndex = spec.stages.length - 1
  if (spec.final_gate && lastIndex >= 0) {
    edges.push({ id: 'e-final', source: stageNodeId(lastIndex), target: 'final', style: { stroke: '#52525b' } })
  }
  if (spec.loop) {
    const fromIndex = spec.stages.findIndex((s) => s.key === spec.loop?.from)
    const toIndex = spec.stages.findIndex((s) => s.key === spec.loop?.to)
    if (fromIndex !== -1 && toIndex !== -1) {
      // петля слева: loop-out (from) → loop-in (to), подпись «i/max_iters»
      edges.push({
        id: 'loop',
        source: stageNodeId(fromIndex),
        sourceHandle: 'loop-out',
        target: stageNodeId(toIndex),
        targetHandle: 'loop-in',
        label: `i/${spec.loop.max_iters}`,
        animated: true,
        style: { stroke: '#fbbf24', strokeDasharray: '6 3' },
        labelStyle: { fill: '#fbbf24', fontSize: 11 },
        labelBgStyle: { fill: '#18181b' },
      })
    }
  }
  return edges
}

export function PipelineCanvas({
  spec,
  errorKeys,
  selectedIndex,
  readOnly,
  onSelect,
}: {
  spec: PipelineSpec
  errorKeys: ReadonlySet<string>
  selectedIndex: number | null
  readOnly: boolean
  onSelect: (index: number | null) => void
}) {
  const [nodes, setNodes] = useState<FlowNode[]>([])
  const edges = useMemo(() => buildEdges(spec), [spec])

  // ноды пересобираются из spec; позиции drag'а сохраняем из предыдущего состояния
  useEffect(() => {
    setNodes((prev) => buildNodes(spec, errorKeys, selectedIndex, prev))
  }, [spec, errorKeys, selectedIndex])

  const onNodesChange = (changes: NodeChange<FlowNode>[]) =>
    setNodes((current) => applyNodeChanges(changes, current))

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      onNodesChange={readOnly ? undefined : onNodesChange}
      onNodeClick={(_, node) => {
        if (node.id.startsWith('s')) onSelect(Number(node.id.slice(1)))
      }}
      onPaneClick={() => onSelect(null)}
      colorMode="dark"
      nodesDraggable={!readOnly}
      nodesConnectable={false}
      elementsSelectable={!readOnly}
      fitView
    >
      <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#3f3f46" />
      <Controls showInteractive={false} />
      <MiniMap pannable zoomable className="!bg-zinc-900" maskColor="rgba(9,9,11,0.7)" nodeColor="#3f3f46" />
    </ReactFlow>
  )
}
