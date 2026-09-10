package supervisor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// m14 (T-30): эвристика открытия гейта lesson_review — полный парсер
// операций: NO_LESSONS/мусор не открывают, LINK-only черновик открывает.
func TestLessonDraftHasOperations(t *testing.T) {
	assert.False(t, lessonDraftHasOperations("NO_LESSONS"))
	assert.False(t, lessonDraftHasOperations("просто текст без карточек"))
	assert.False(t, lessonDraftHasOperations(""))

	card := "---\ntitle: урок\ntriggers: [a]\n---\n\n## Причина (почему)\nx\n\n## Правило\ny\n"
	assert.True(t, lessonDraftHasOperations(card))

	linkOnly := "---\nop: LINK\ntarget: lesson-1\ntarget2: lesson-2\n---\n"
	assert.True(t, lessonDraftHasOperations(linkOnly), "LINK-only черновик открывает гейт")

	brokenRefine := "---\nop: REFINE\ntitle: нет target\n---\n\ntело\n"
	assert.False(t, lessonDraftHasOperations(brokenRefine), "битая операция не считается")
}
