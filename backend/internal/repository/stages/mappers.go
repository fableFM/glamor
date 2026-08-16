package stages

import (
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func mapStageToDTO(s stage) dtorep.Stage {
	out := dtorep.Stage{
		ID:          s.id,
		RunID:       s.runID,
		StageKey:    s.stageKey,
		Iteration:   s.iteration,
		State:       dtorep.StageState(s.state),
		Harness:     s.harness,
		ResumeCount: s.resumeCount,
		TokensIn:    s.tokensIn,
		TokensOut:   s.tokensOut,
	}
	if s.sessionID.Valid {
		out.SessionID = &s.sessionID.String
	}
	if s.pid.Valid {
		out.PID = &s.pid.Int64
	}
	if s.exitCode.Valid {
		out.ExitCode = &s.exitCode.Int64
	}
	if s.stopRequestedBy.Valid {
		out.StopRequestedBy = &s.stopRequestedBy.String
	}
	if s.startedAt.Valid {
		t := s.startedAt.Time
		out.StartedAt = &t
	}
	if s.finishedAt.Valid {
		t := s.finishedAt.Time
		out.FinishedAt = &t
	}
	if s.err.Valid {
		out.Error = &s.err.String
	}
	return out
}
