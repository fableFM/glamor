// Package http — REST-контроллер демона (T-05): strict-server поверх
// сгенерированного oapi-codegen интерфейса (D-02), auth-мидлварь (D-08),
// единый формат ошибок Error{code,message,details?}.
package http

import (
	"encoding/json"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// --- dtorep → genapi --------------------------------------------------------

func mapProject(p *dtorep.Project) genapi.Project {
	return genapi.Project{
		Id:            p.ID,
		Path:          p.Path,
		Name:          p.Name,
		DefaultBranch: p.DefaultBranch,
		IdeCommand:    p.IDECommand,
		CreatedAt:     p.CreatedAt,
	}
}

func mapPipeline(p *dtorep.Pipeline) genapi.Pipeline {
	return genapi.Pipeline{
		Id:              p.ID,
		ProjectId:       p.ProjectID,
		Name:            p.Name,
		Version:         p.Version,
		ParentVersionId: p.ParentVersionID,
		SpecJson:        p.SpecJSON,
		CreatedAt:       p.CreatedAt,
	}
}

func mapRun(r *dtorep.Run) genapi.Run {
	return genapi.Run{
		Id:                r.ID,
		ProjectId:         r.ProjectID,
		PipelineVersionId: r.PipelineVersionID,
		TaskText:          r.TaskText,
		BaseBranch:        r.BaseBranch,
		Branch:            r.Branch,
		State:             genapi.RunState(r.State),
		Depth:             r.Depth,
		NotifyTg:          r.NotifyTG,
		CreatedAt:         r.CreatedAt,
		FinishedAt:        r.FinishedAt,
	}
}

func mapStage(s *dtorep.Stage) genapi.Stage {
	return genapi.Stage{
		Id:              s.ID,
		RunId:           s.RunID,
		StageKey:        s.StageKey,
		Iteration:       s.Iteration,
		State:           genapi.StageState(s.State),
		Harness:         s.Harness,
		SessionId:       s.SessionID,
		Pid:             s.PID,
		ExitCode:        s.ExitCode,
		StopRequestedBy: s.StopRequestedBy,
		ResumeCount:     s.ResumeCount,
		StartedAt:       s.StartedAt,
		FinishedAt:      s.FinishedAt,
		TokensIn:        s.TokensIn,
		TokensOut:       s.TokensOut,
		Error:           s.Error,
	}
}

func mapGate(g *dtorep.Gate) genapi.Gate {
	out := genapi.Gate{
		Id:         g.ID,
		RunId:      g.RunID,
		StageId:    g.StageID,
		Kind:       genapi.GateKind(g.Kind),
		Question:   g.Question,
		State:      genapi.GateState(g.State),
		Answer:     g.Answer,
		CreatedAt:  g.CreatedAt,
		ResolvedAt: g.ResolvedAt,
	}
	if g.ContextJSON != "" {
		out.ContextJson = &g.ContextJSON
	}
	return out
}

func mapArtifact(a *dtorep.Artifact) genapi.Artifact {
	return genapi.Artifact{
		Id:        a.ID,
		RunId:     a.RunID,
		StageId:   a.StageID,
		Path:      a.Path,
		Kind:      a.Kind,
		CreatedAt: a.CreatedAt,
	}
}

func mapNote(n *dtorep.Note) genapi.Note {
	kind := genapi.NoteKind(n.Kind)
	return genapi.Note{
		Id:        n.ID,
		RunId:     n.RunID,
		StageId:   n.StageID,
		Kind:      &kind,
		Text:      n.Text,
		Consumed:  n.Consumed,
		CreatedAt: n.CreatedAt,
	}
}

func mapEvent(e *dtorep.Event) genapi.Event {
	payload := genapi.EventPayload{}
	if e.PayloadJSON != "" {
		// payload в журнале всегда валидный JSON (писали мы сами);
		// на всякий случай деградируем в raw-строку, а не в 500
		if err := json.Unmarshal([]byte(e.PayloadJSON), &payload); err != nil {
			payload = genapi.EventPayload{"raw": e.PayloadJSON}
		}
	}
	return genapi.Event{
		Id:      e.ID,
		RunId:   e.RunID,
		StageId: e.StageID,
		Ts:      e.TS,
		Kind:    genapi.EventKind(e.Kind),
		Payload: payload,
	}
}

func mapSlice[T, R any](in []T, fn func(*T) R) []R {
	out := make([]R, 0, len(in))
	for i := range in {
		out = append(out, fn(&in[i]))
	}
	return out
}
