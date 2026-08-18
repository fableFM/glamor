package http

import (
	"context"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// --- уроки (D-52, T-29) ------------------------------------------------------

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
	out.RelapseCount = &l.RelapseCount
	if l.ProjectID != nil {
		out.ProjectId = l.ProjectID
	}
	if l.RunID != nil {
		out.RunId = l.RunID
	}
	out.StageKey = &l.StageKey
	return out
}

func (h *handlers) ListLessons(ctx context.Context, req genapi.ListLessonsRequestObject) (genapi.ListLessonsResponseObject, error) {
	var status, scope string
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if req.Params.Scope != nil {
		scope = string(*req.Params.Scope)
	}

	lessons, err := h.lessons.List(ctx, status, scope, req.Params.ProjectId)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.ListLessonsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.ListLessons200JSONResponse(mapSlice(lessons, mapLesson)), nil
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
	return genapi.GetLesson200JSONResponse(genapi.LessonDetail{
		Id:           mapped.Id,
		Title:        mapped.Title,
		Scope:        genapi.LessonDetailScope(mapped.Scope),
		Status:       genapi.LessonDetailStatus(mapped.Status),
		Path:         mapped.Path,
		ProjectId:    mapped.ProjectId,
		RunId:        mapped.RunId,
		StageKey:     mapped.StageKey,
		AppliedCount: mapped.AppliedCount,
		RelapseCount: mapped.RelapseCount,
		CreatedAt:    mapped.CreatedAt,
		Content:      content,
	}), nil
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
