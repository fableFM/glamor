package http

import (
	"context"
	"fmt"
	"strings"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/service/catalog"
)

// --- метрики (T-24) ----------------------------------------------------------

func mapStageMetrics(m catalog.StageMetrics) genapi.StageMetrics {
	out := genapi.StageMetrics{
		StageId:     m.StageID,
		StageKey:    m.StageKey,
		Iteration:   m.Iteration,
		State:       genapi.StageState(m.State),
		TokensIn:    m.TokensIn,
		TokensOut:   m.TokensOut,
		ResumeCount: m.ResumeCount,
		ExitCode:    m.ExitCode,
	}
	if m.DurationSec != nil {
		out.DurationSec = ptrFloat32(float32(*m.DurationSec))
	}
	return out
}

func ptrFloat32(v float32) *float32 { return &v }

func (h *handlers) GetRunMetrics(ctx context.Context, req genapi.GetRunMetricsRequestObject) (genapi.GetRunMetricsResponseObject, error) {
	m, err := h.api.RunMetrics(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetRunMetricsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	resp := genapi.GetRunMetrics200JSONResponse{
		RunId:           m.RunID,
		GateWaitSeconds: float32(m.GateWaitSeconds),
	}
	resp.Totals.StagesCount = m.StagesCount
	resp.Totals.TokensIn = m.TokensIn
	resp.Totals.TokensOut = m.TokensOut
	if m.DurationSec != nil {
		d := float32(*m.DurationSec)
		resp.Totals.DurationSec = &d
	}
	for _, st := range m.Stages {
		resp.Stages = append(resp.Stages, mapStageMetrics(st))
	}
	return resp, nil
}

func (h *handlers) GetRunMetricsCsv(ctx context.Context, req genapi.GetRunMetricsCsvRequestObject) (genapi.GetRunMetricsCsvResponseObject, error) {
	m, err := h.api.RunMetrics(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetRunMetricsCsvdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	var b strings.Builder
	b.WriteString("stage_key,iteration,state,tokens_in,tokens_out,duration_sec,resume_count,exit_code\n")
	for _, st := range m.Stages {
		duration := ""
		if st.DurationSec != nil {
			duration = fmt.Sprintf("%.1f", *st.DurationSec)
		}
		exitCode := ""
		if st.ExitCode != nil {
			exitCode = fmt.Sprintf("%d", *st.ExitCode)
		}
		fmt.Fprintf(&b, "%s,%d,%s,%d,%d,%s,%d,%s\n",
			st.StageKey, st.Iteration, st.State, st.TokensIn, st.TokensOut,
			duration, st.ResumeCount, exitCode)
	}
	return genapi.GetRunMetricsCsv200TextcsvResponse{Body: strings.NewReader(b.String())}, nil
}

func (h *handlers) GetProjectMetrics(ctx context.Context, req genapi.GetProjectMetricsRequestObject) (genapi.GetProjectMetricsResponseObject, error) {
	period := "7d"
	if req.Params.Period != nil {
		period = string(*req.Params.Period)
	}

	m, err := h.api.ProjectMetrics(ctx, req.Id, period)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetProjectMetricsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	byState := map[string]int{}
	for k, v := range m.ByState {
		byState[k] = v
	}
	return genapi.GetProjectMetrics200JSONResponse{
		ProjectId:        m.ProjectID,
		Period:           m.Period,
		RunsTotal:        m.RunsTotal,
		TokensIn:         m.TokensIn,
		TokensOut:        m.TokensOut,
		TotalDurationSec: ptrFloat32(float32(m.TotalDurationSec)),
		ByState:          byState,
	}, nil
}
