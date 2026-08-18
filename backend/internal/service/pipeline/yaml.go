package pipeline

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// YAML-формат пайплайна (T-21): человекочитаемый, одна версия = один
// файл, стабильный порядок ключей (порядок полей struct — порядок в YAML).
type yamlPipeline struct {
	Name    string `yaml:"name"`
	Version int64  `yaml:"version"`
	// ParentVersionID — только в экспорте существующей версии (при импорте
	// вычисляется заново по имени/флагу on_conflict).
	ParentVersionID *int64      `yaml:"parent_version_id,omitempty"`
	Stages          []yamlStage `yaml:"stages"`
	Loop            *yamlLoop   `yaml:"loop,omitempty"`
	FinalGate       string      `yaml:"final_gate,omitempty"`
}

type yamlStage struct {
	Key            string        `yaml:"key"`
	Kind           string        `yaml:"kind,omitempty"`
	Harness        string        `yaml:"harness,omitempty"`
	Model          string        `yaml:"model,omitempty"`
	Effort         string        `yaml:"effort,omitempty"`
	Artifact       *yamlArtifact `yaml:"artifact,omitempty"`
	QuestionsPath  string        `yaml:"questions_path,omitempty"`
	GateAfter      string        `yaml:"gate_after,omitempty"`
	PromptTemplate string        `yaml:"prompt_template,omitempty"`
}

type yamlArtifact struct {
	Path     string `yaml:"path"`
	Required bool   `yaml:"required"`
}

type yamlLoop struct {
	From     string `yaml:"from"`
	To       string `yaml:"to"`
	MaxIters int64  `yaml:"max_iters"`
}

// specJSONToYAML — экспорт: spec_json → YAML.
func specJSONToYAML(name string, version int64, parentVersionID *int64, specJSON string) (string, error) {
	var spec struct {
		Stages []struct {
			Key      string `json:"key"`
			Kind     string `json:"kind"`
			Harness  string `json:"harness"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
			Artifact *struct {
				Path     string `json:"path"`
				Required bool   `json:"required"`
			} `json:"artifact"`
			QuestionsPath  string `json:"questions_path"`
			GateAfter      string `json:"gate_after"`
			PromptTemplate string `json:"prompt_template"`
		} `json:"stages"`
		Loop *struct {
			From     string `json:"from"`
			To       string `json:"to"`
			MaxIters int64  `json:"max_iters"`
		} `json:"loop"`
		FinalGate string `json:"final_gate"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return "", fmt.Errorf("failed to parse spec_json: %w", err)
	}

	out := yamlPipeline{
		Name:            name,
		Version:         version,
		ParentVersionID: parentVersionID,
		FinalGate:       spec.FinalGate,
	}
	for _, st := range spec.Stages {
		yst := yamlStage{
			Key:            st.Key,
			Kind:           st.Kind,
			Harness:        st.Harness,
			Model:          st.Model,
			Effort:         st.Effort,
			QuestionsPath:  st.QuestionsPath,
			GateAfter:      st.GateAfter,
			PromptTemplate: st.PromptTemplate,
		}
		if st.Artifact != nil {
			yst.Artifact = &yamlArtifact{Path: st.Artifact.Path, Required: st.Artifact.Required}
		}
		out.Stages = append(out.Stages, yst)
	}
	if spec.Loop != nil {
		out.Loop = &yamlLoop{From: spec.Loop.From, To: spec.Loop.To, MaxIters: spec.Loop.MaxIters}
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("failed to marshal pipeline yaml: %w", err)
	}
	return string(data), nil
}

// yamlToSpecJSON — импорт: YAML → spec_json (с валидацией, T-21/T-20).
func yamlToSpecJSON(yamlData string) (name, specJSON string, err error) {
	var yp yamlPipeline
	if err := yaml.Unmarshal([]byte(yamlData), &yp); err != nil {
		return "", "", fmt.Errorf("failed to parse pipeline yaml: %w", err)
	}
	if yp.Name == "" {
		return "", "", fmt.Errorf("pipeline yaml: name is empty: %w", errValidation)
	}

	stages := make([]map[string]any, 0, len(yp.Stages))
	for _, st := range yp.Stages {
		m := map[string]any{}
		put := func(k, v string) {
			if v != "" {
				m[k] = v
			}
		}
		put("key", st.Key)
		put("kind", st.Kind)
		put("harness", st.Harness)
		put("model", st.Model)
		put("effort", st.Effort)
		put("questions_path", st.QuestionsPath)
		put("gate_after", st.GateAfter)
		put("prompt_template", st.PromptTemplate)
		if st.Artifact != nil {
			m["artifact"] = map[string]any{"path": st.Artifact.Path, "required": st.Artifact.Required}
		}
		stages = append(stages, m)
	}

	spec := map[string]any{"stages": stages}
	if yp.Loop != nil {
		spec["loop"] = map[string]any{"from": yp.Loop.From, "to": yp.Loop.To, "max_iters": yp.Loop.MaxIters}
	}
	if yp.FinalGate != "" {
		spec["final_gate"] = yp.FinalGate
	}

	data, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal spec_json: %w", err)
	}

	if err := ValidateSpec(string(data)); err != nil {
		return "", "", err
	}
	return yp.Name, string(data), nil
}
