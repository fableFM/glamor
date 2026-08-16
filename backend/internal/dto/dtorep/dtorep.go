// Package dtorep — DTO репозиторного слоя (D-80): все типы, пересекающие
// границу repository ↔ service. Модели БД приватны в пакетах
// internal/repository/<domain>, наружу отдаются только эти типы.
package dtorep

import "time"

// RunState — состояние рана (ADR-001).
type RunState string

const (
	RunStateDraft       RunState = "draft"
	RunStateRunning     RunState = "running"
	RunStateWaitingGate RunState = "waiting_gate"
	RunStateSucceeded   RunState = "succeeded"
	RunStateFailed      RunState = "failed"
	RunStateStopped     RunState = "stopped"
)

// StageState — состояние стадии рана (ADR-001).
type StageState string

const (
	StageStatePending     StageState = "pending"
	StageStateRunning     StageState = "running"
	StageStateSucceeded   StageState = "succeeded"
	StageStateFailed      StageState = "failed"
	StageStateInterrupted StageState = "interrupted"
	StageStateSkipped     StageState = "skipped"
)

// GateKind — вид гейта (D-21).
type GateKind string

const (
	GateKindPlanApproval GateKind = "plan_approval"
	GateKindQuestion     GateKind = "question"
	GateKindEscalation   GateKind = "escalation"
	GateKindFinalReview  GateKind = "final_review"
)

// GateState — состояние гейта (ADR-001).
type GateState string

const (
	GateStateOpen     GateState = "open"
	GateStateAnswered GateState = "answered"
	GateStateApproved GateState = "approved"
	GateStateRejected GateState = "rejected"
	GateStateExpired  GateState = "expired"
)

// Project — проект (каталог с git-репозиторием).
type Project struct {
	ID            int64
	Path          string
	Name          string
	DefaultBranch string
	IDECommand    string
	CreatedAt     time.Time
}

type CreateProjectRequest struct {
	Path          string
	Name          string
	DefaultBranch string
	IDECommand    string
}

// Pipeline — версия пайплайна. Версии неизменяемы: правка = новая версия
// с ParentVersionID на предыдущую (T-21).
type Pipeline struct {
	ID              int64
	ProjectID       *int64 // NULL = глобальный пайплайн
	Name            string
	Version         int64
	ParentVersionID *int64
	SpecJSON        string
	CreatedAt       time.Time
}

type CreatePipelineRequest struct {
	ProjectID       *int64
	Name            string
	Version         int64
	ParentVersionID *int64
	SpecJSON        string
}

// Run — экземпляр выполнения пайплайна над проектом.
type Run struct {
	ID                string
	ProjectID         int64
	PipelineVersionID int64
	TaskText          string
	BaseBranch        string
	Branch            string
	State             RunState
	Depth             int64
	NotifyTG          bool
	IdempotencyKey    string
	CreatedAt         time.Time
	FinishedAt        *time.Time
}

type CreateRunRequest struct {
	ID                string // uuid, генерируется вызывающим слоем
	ProjectID         int64
	PipelineVersionID int64
	TaskText          string
	BaseBranch        string
	Branch            string
	State             RunState
	Depth             int64
	NotifyTG          bool
	IdempotencyKey    string
}

// ListRunsRequest — динамический фильтр списка ранов (go-sqlbuilder).
type ListRunsRequest struct {
	ProjectID         *int64
	PipelineVersionID *int64
	States            []RunState
	Limit             int // 0 = без лимита
}

// Stage — попытка выполнения этапа. Каждая попытка — отдельная строка
// (run_id, stage_key, iteration); см. ADR-001 (семантика interrupted/resume).
type Stage struct {
	ID              int64
	RunID           string
	StageKey        string
	Iteration       int64
	State           StageState
	Harness         string
	SessionID       *string
	PID             *int64
	ExitCode        *int64
	StopRequestedBy *string
	ResumeCount     int64
	StartedAt       *time.Time
	FinishedAt      *time.Time
	TokensIn        int64
	TokensOut       int64
	Error           *string
}

type CreateStageRequest struct {
	RunID       string
	StageKey    string
	Iteration   int64
	Harness     string
	ResumeCount int64
}

// StageTransitionFields — поля, выставляемые при CAS-переходе стадии.
// nil = не трогаем колонку.
type StageTransitionFields struct {
	SessionID       *string
	PID             *int64
	ExitCode        *int64
	StopRequestedBy *string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	Error           *string
	TokensIn        *int64
	TokensOut       *int64
}

// Gate — first-class гейт (D-21): approve/reject/answer/comment.
type Gate struct {
	ID             string
	RunID          string
	StageID        *int64
	Kind           GateKind
	Question       string
	ContextJSON    string
	State          GateState
	Answer         *string
	IdempotencyKey string
	CreatedAt      time.Time
	ResolvedAt     *time.Time
}

type CreateGateRequest struct {
	ID             string
	RunID          string
	StageID        *int64
	Kind           GateKind
	Question       string
	ContextJSON    string
	IdempotencyKey string
}

// RunIDAll — специальный run_id журнала для «все раны» (wildcard-подписка
// /ws, инбокс гейтов). Доменная константа журнала: используется
// репозиторием events, шиной events и контроллерами.
const RunIDAll = "*"

// Event — событие append-only журнала (D-11).
type Event struct {
	ID          int64
	RunID       string
	StageID     *int64
	TS          time.Time
	Kind        string
	PayloadJSON string
}

// Artifact — файл-артефакт этапа (D-13: источник истины о завершении).
type Artifact struct {
	ID        int64
	RunID     string
	StageID   *int64
	Path      string
	Kind      string
	CreatedAt time.Time
}

type CreateArtifactRequest struct {
	RunID   string
	StageID *int64
	Path    string
	Kind    string
}

// PatchProjectRequest — частичное обновление проекта (nil = не трогаем).
type PatchProjectRequest struct {
	DefaultBranch *string
	IDECommand    *string
}

// NoteKind — вид заметки (D-22).
type NoteKind string

const (
	// NoteKindNote — queue note к ближайшему событию рана.
	NoteKindNote NoteKind = "note"
	// NoteKindSteer — steer-сообщение Interrupt&Steer: резюм сессии с ним.
	NoteKindSteer NoteKind = "steer"
)

// Note — заметка к рану/этапу (очередь с consumed-флагом, D-22).
type Note struct {
	ID             int64
	RunID          string
	StageID        *int64
	Kind           NoteKind
	Text           string
	Consumed       bool
	IdempotencyKey string
	CreatedAt      time.Time
	ConsumedAt     *time.Time
}

type CreateNoteRequest struct {
	RunID          string
	StageID        *int64
	Kind           NoteKind
	Text           string
	IdempotencyKey string
}
