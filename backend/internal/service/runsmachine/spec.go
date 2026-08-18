package runsmachine

import (
	"encoding/json"
	"fmt"
)

// Spec — спецификация пайплайна (pipelines.spec_json). Полная схема
// (janitor, сложные рёбра) — T-20/T-22; для M1: упорядоченные этапы +
// одна fix-петля + финальный гейт (T-17).
type Spec struct {
	Stages []StageSpec `json:"stages"`
	// Loop — fix-петля (D-23): verdict этапа From == changes_required →
	// ре-вход To (fixer), затем снова From; исчерпание MaxIters →
	// эскалация-гейт. Вычисляется pipeline-хуком supervisor'а (T-17).
	Loop *LoopSpec `json:"loop,omitempty"`
	// FinalGate — гейт перед терминальным succeeded (final_review, D-21).
	FinalGate string `json:"final_gate,omitempty"`
	// ReviewPolicy — строгость fix-петли (D-23): "blocking" (только
	// blocking), "major" (blocking+major, дефолт), "all" (любой finding
	// → changes_required). Сравнивается со severity findings verdict.json.
	ReviewPolicy string `json:"review_policy,omitempty"`
	// MemoryScope — политика записи vendor-памяти (T-23): "auto" (дефолт:
	// локальная+глобальная) | "gate" (только локальная, промоушн кнопкой).
	MemoryScope string `json:"memory_scope,omitempty"`
	// ParallelGroups — fan-out ветки (T-28): группы этапов, выполняемых
	// параллельно (только read_only этапы, ADR-003).
	ParallelGroups []ParallelGroupSpec `json:"parallel_groups,omitempty"`
}

// ParallelGroupSpec — группа параллельных веток (T-28, ADR-003).
type ParallelGroupSpec struct {
	Name string `json:"name"`
	// OnFailure — политика при падении ветки: "fail_fast" (дефолт: первая
	// failed → ран failed) | "wait_all" (дождаться остальных → failed).
	OnFailure string `json:"on_failure,omitempty"`
}

// LoopSpec — ребро fix-петли (D-23, T-17).
type LoopSpec struct {
	From     string `json:"from"` // этап с verdict-артефактом (reviewer)
	To       string `json:"to"`   // этап исправлений (fixer)
	MaxIters int64  `json:"max_iters"`
}

// StageSpec — описание одного этапа пайплайна.
type StageSpec struct {
	Key     string `json:"key"`
	Kind    string `json:"kind,omitempty"` // llm-stage (M1), janitor/human-gate — M2
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	// Artifact — контракт артефакта этапа (D-13): источник истины о
	// завершении вместе с exit code.
	Artifact *ArtifactRef `json:"artifact,omitempty"`
	// QuestionsPath — путь к questions.md (D-20 «вопросы как артефакт»):
	// этап завершился, файл существует и непуст → гейт question.
	// Плейсхолдер {run_id} раскрывается supervisor'ом.
	QuestionsPath string `json:"questions_path,omitempty"`
	// GateAfter — гейт после успешного этапа БЕЗ вопросов
	// (plan_approval/final_review/...), T-11.
	GateAfter string `json:"gate_after,omitempty"`
	// PromptTemplate — inline-шаблон промпта этапа (T-17), плейсхолдеры
	// {{task}}, {{artifact.X}}, {{depth}}, {{verdict}}, ... (рендер —
	// internal/service/pipeline).
	PromptTemplate string `json:"prompt_template,omitempty"`

	// --- janitor-нода (kind="janitor", T-22): детерминированные команды ---
	// Commands — список shell-команд (доверенная конфигурация пайплайна,
	// НЕ вывод LLM — ядро никогда не исполняет строки модели).
	Commands []string `json:"commands,omitempty"`
	// OnFail — политика при ненулевом exit: "fail_stage" (дефолт) | "warn".
	OnFail string `json:"on_fail,omitempty"`
	// CommandTimeoutSec — таймаут одной команды (0 → дефолт supervisor'а).
	CommandTimeoutSec int `json:"command_timeout_sec,omitempty"`

	// ParallelGroup — имя группы параллельного выполнения (T-28);
	// пусто — обычный линейный этап.
	ParallelGroup string `json:"parallel_group,omitempty"`
	// ReadOnly — этап не пишет в чекаут (исследование/планирование);
	// обязателен для параллельных этапов (ADR-003).
	ReadOnly bool `json:"read_only,omitempty"`
}

// ArtifactRef — ссылка на обязательный артефакт этапа в спеке пайплайна.
type ArtifactRef struct {
	Path     string `json:"path"` // относительно чекаута проекта
	Required bool   `json:"required"`
}

// ParseSpec разбирает spec_json версии пайплайна.
func ParseSpec(specJSON string) (Spec, error) {
	var spec Spec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return Spec{}, fmt.Errorf("failed to parse pipeline spec: %w", err)
	}
	if len(spec.Stages) == 0 {
		return Spec{}, fmt.Errorf("failed to parse pipeline spec: no stages")
	}
	seen := make(map[string]struct{}, len(spec.Stages))
	for _, st := range spec.Stages {
		if st.Key == "" {
			return Spec{}, fmt.Errorf("failed to parse pipeline spec: stage with empty key")
		}
		if _, dup := seen[st.Key]; dup {
			return Spec{}, fmt.Errorf("failed to parse pipeline spec: duplicate stage key %q", st.Key)
		}
		seen[st.Key] = struct{}{}
	}
	return spec, nil
}
