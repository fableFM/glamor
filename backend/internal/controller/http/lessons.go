package http

import (
	"context"
	"encoding/json"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// --- уроки (D-52, T-29; эволюция — T-30) ---------------------------------------

func mapLesson(l *dtorep.Lesson) genapi.Lesson {
	out := genapi.Lesson{
		Id:        l.ID,
		Title:     l.Title,
		Scope:     genapi.LessonScope(l.Scope),
		Status:    genapi.LessonStatus(l.Status),
		Path:      l.Path,
		CreatedAt: l.CreatedAt,
	}
	out.AppliedCount = &l.AppliedCount
	out.AppliedSuccessCount = &l.AppliedSuccessCount
	out.RelapseCount = &l.RelapseCount
	out.Importance = &l.Importance
	if l.Kind != "" {
		kind := genapi.LessonKind(l.Kind)
		out.Kind = &kind
	}
	if l.ProjectID != nil {
		out.ProjectId = l.ProjectID
	}
	if l.RunID != nil {
		out.RunId = l.RunID
	}
	out.StageKey = &l.StageKey
	out.Vendor = l.Vendor
	out.VendorVersion = l.VendorVersion
	out.Area = l.Area
	out.SupersededBy = l.SupersededBy
	if related := parseRelated(l.RelatedJSON); related != nil {
		out.Related = &related
	}
	return out
}

// parseRelated — related_json → срез id (битый JSON → nil).
func parseRelated(relatedJSON string) []string {
	if relatedJSON == "" {
		return nil
	}
	var related []string
	if err := json.Unmarshal([]byte(relatedJSON), &related); err != nil || len(related) == 0 {
		return nil
	}
	return related
}

func (h *handlers) ListLessons(ctx context.Context, req genapi.ListLessonsRequestObject) (genapi.ListLessonsResponseObject, error) {
	var status, scope, kind string
	var attention bool
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if req.Params.Scope != nil {
		scope = string(*req.Params.Scope)
	}
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	if req.Params.Attention != nil {
		attention = *req.Params.Attention
	}

	lessons, err := h.lessons.ListFiltered(ctx, status, scope, kind, attention, req.Params.ProjectId)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.ListLessonsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.ListLessons200JSONResponse(mapSlice(lessons, mapLesson)), nil
}

// LessonConsolidationCandidates — кандидаты на консолидацию (T-30):
// дубли, протухшие superseded, нездоровые уроки.
func (h *handlers) LessonConsolidationCandidates(ctx context.Context, req genapi.LessonConsolidationCandidatesRequestObject) (genapi.LessonConsolidationCandidatesResponseObject, error) {
	days := 0
	if req.Params.SupersededDays != nil {
		days = *req.Params.SupersededDays
	}
	report, err := h.lessons.ConsolidationCandidates(ctx, days)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.LessonConsolidationCandidatesdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	out := genapi.LessonConsolidation{
		Duplicates:      make([]genapi.LessonDuplicatePair, 0, len(report.Duplicates)),
		StaleSuperseded: make([]genapi.Lesson, 0, len(report.StaleSuperseded)),
		Unhealthy:       make([]genapi.Lesson, 0, len(report.Unhealthy)),
	}
	for _, p := range report.Duplicates {
		lesson := p.Lesson
		similarTo := p.SimilarTo
		out.Duplicates = append(out.Duplicates, genapi.LessonDuplicatePair{
			Lesson:    mapLesson(&lesson),
			SimilarTo: mapLesson(&similarTo),
			Reason:    p.Reason,
		})
	}
	for i := range report.StaleSuperseded {
		out.StaleSuperseded = append(out.StaleSuperseded, mapLesson(&report.StaleSuperseded[i]))
	}
	for i := range report.Unhealthy {
		out.Unhealthy = append(out.Unhealthy, mapLesson(&report.Unhealthy[i]))
	}
	return genapi.LessonConsolidationCandidates200JSONResponse(out), nil
}

func (h *handlers) GetLesson(ctx context.Context, req genapi.GetLessonRequestObject) (genapi.GetLessonResponseObject, error) {
	lesson, err := h.lessons.GetByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetLessondefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	content, err := h.lessons.ReadContent(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetLessondefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	mapped := mapLesson(lesson)
	detail := genapi.LessonDetail{
		Id:                  mapped.Id,
		Title:               mapped.Title,
		Scope:               genapi.LessonDetailScope(mapped.Scope),
		Status:              genapi.LessonDetailStatus(mapped.Status),
		Path:                mapped.Path,
		ProjectId:           mapped.ProjectId,
		RunId:               mapped.RunId,
		StageKey:            mapped.StageKey,
		Vendor:              mapped.Vendor,
		VendorVersion:       mapped.VendorVersion,
		Area:                mapped.Area,
		SupersededBy:        mapped.SupersededBy,
		Related:             mapped.Related,
		Importance:          mapped.Importance,
		AppliedCount:        mapped.AppliedCount,
		AppliedSuccessCount: mapped.AppliedSuccessCount,
		RelapseCount:        mapped.RelapseCount,
		CreatedAt:           mapped.CreatedAt,
		Content:             content,
	}
	if mapped.Kind != nil {
		kind := genapi.LessonDetailKind(*mapped.Kind)
		detail.Kind = &kind
	}
	return genapi.GetLesson200JSONResponse(detail), nil
}

func (h *handlers) PatchLesson(ctx context.Context, req genapi.PatchLessonRequestObject) (genapi.PatchLessonResponseObject, error) {
	if err := h.lessons.SetStatus(ctx, req.Id, string(req.Body.Status)); err != nil {
		e, status := errorToResponse(err)
		return genapi.PatchLessondefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	lesson, err := h.lessons.GetByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.PatchLessondefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.PatchLesson200JSONResponse(mapLesson(lesson)), nil
}
