package runsmachine

import (
	"encoding/json"
	"fmt"
)

// Spec — спецификация пайплайна (pipelines.spec_json). Полная схема
// (гейты, loop-рёбра, janitor) определяется в T-17/T-20; машине для
// линейного продвижения достаточно упорядоченного списка этапов.
type Spec struct {
	Stages []StageSpec `json:"stages"`
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
