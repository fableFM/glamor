package telegram

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	telegramrep "github.com/fableFM/glamor/internal/repository/telegram"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/pkg/uuid"
)

// --- in-memory мок Bot API ----------------------------------------------------

type sentMessage struct {
	id  int64
	req SendMessageRequest
}

type callbackAnswer struct {
	id   string
	text string
}

type mockBot struct {
	mu        sync.Mutex
	sent      []sentMessage
	answers   []callbackAnswer
	nextMsgID int64
	updates   chan Update
	sendErr   error // инъекция сбоя отправки (F-06)
}

func newMockBot() *mockBot {
	return &mockBot{updates: make(chan Update, 128), nextMsgID: 1}
}

func (m *mockBot) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	select {
	case u := <-m.updates:
		return []Update{u}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Millisecond):
		return nil, nil
	}
}

func (m *mockBot) SendMessage(_ context.Context, req SendMessageRequest) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return 0, m.sendErr
	}
	id := m.nextMsgID
	m.nextMsgID++
	m.sent = append(m.sent, sentMessage{id: id, req: req})
	return id, nil
}

// setSendErr включает/выключает сбой отправки (F-06).
func (m *mockBot) setSendErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendErr = err
}

func (m *mockBot) AnswerCallbackQuery(_ context.Context, id, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers = append(m.answers, callbackAnswer{id: id, text: text})
	return nil
}

func (m *mockBot) snapshot() []sentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMessage(nil), m.sent...)
}

func (m *mockBot) answersSnapshot() []callbackAnswer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]callbackAnswer(nil), m.answers...)
}

// --- фикстура -----------------------------------------------------------------

const testChatID = 1001

type tgFixture struct {
	db      *sql.DB
	bot     *mockBot
	adapter *Adapter
	machine *runsmachine.Machine
	runsSvc *runsapi.Service
	tg      telegramrep.RepositoryWithTX
	runs    runsrep.RepositoryWithTX
	stages  stagesrep.RepositoryWithTX
	gates   gatesrep.RepositoryWithTX
	notes   notesrep.RepositoryWithTX
	journal *events.Journal

	updateSeq int64
	cancel    context.CancelFunc
	done      chan struct{}
}

// newTgFixture собирает ядро (как main, ручной DI) + адаптер с моком Bot API.
// withChat — чат сразу в whitelist (false — для сценария привязки).
func newTgFixture(t *testing.T, withChat bool) *tgFixture {
	t.Helper()
	ctx := context.Background()

	db := testdb.New(t)
	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)

	runsRepo := runsrep.NewRepository(db)
	stagesRepo := stagesrep.NewRepository(db)
	gatesRepo := gatesrep.NewRepository(db)
	pipelinesRepo := pipelinesrep.NewRepository(db)
	projectsRepo := projectsrep.NewRepository(db)
	notesRepo := notesrep.NewRepository(db)
	tgRepo := telegramrep.NewRepository(db)

	machine := runsmachine.NewMachine(runsRepo, stagesRepo, gatesRepo,
		pipelinesRepo, projectsRepo, notesRepo, journal, journal)
	runsSvc := runsapi.New(machine, journal, journal,
		runsRepo, stagesRepo, notesRepo, projectsRepo, pipelinesRepo, gatesRepo)

	if withChat {
		require.NoError(t, tgRepo.AddChat(ctx, testChatID))
	}

	bot := newMockBot()
	catalogSvc := catalog.New(projectsRepo, pipelinesRepo, runsRepo, stagesRepo,
		gatesRepo, artifactsrep.NewRepository(db), notesRepo, journal, t.TempDir())
	adapter := NewAdapter(bot, hub, journal, runsSvc, catalogSvc, tgRepo, Config{
		PollTimeoutSec:     1,
		PollRetryDelay:     10 * time.Millisecond,
		GlobalSendInterval: time.Nanosecond, // троттлинг выключен в тестах
		ChatSendInterval:   time.Nanosecond,
	})

	f := &tgFixture{
		db: db, bot: bot, adapter: adapter, machine: machine, runsSvc: runsSvc,
		tg: tgRepo, runs: runsRepo, stages: stagesRepo, gates: gatesRepo,
		notes: notesRepo, journal: journal,
	}
	return f
}

// start запускает адаптер (poller+sender) с остановкой в cleanup.
func (f *tgFixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		_ = f.adapter.Run(ctx)
		close(f.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-f.done
	})
}

// seedRun — project + pipeline + run (draft) с заданным notify_tg.
func seedRun(t *testing.T, db *sql.DB, notifyTG bool) string {
	t.Helper()
	ctx := context.Background()

	projectID, err := projectsrep.NewRepository(db).CreateProject(ctx, dtorep.CreateProjectRequest{
		Path:          "/tmp/" + uuid.New(),
		Name:          "test-project",
		DefaultBranch: "main",
	})
	require.NoError(t, err)

	pipelineID, err := pipelinesrep.NewRepository(db).CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name:     "default",
		Version:  1,
		SpecJSON: `{"stages":[{"key":"plan","harness":"fake"}]}`,
	})
	require.NoError(t, err)

	runID := uuid.New()
	err = runsrep.NewRepository(db).CreateRun(ctx, dtorep.CreateRunRequest{
		ID:                runID,
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "test task",
		BaseBranch:        "main",
		Branch:            "glamor/test",
		State:             dtorep.RunStateDraft,
		NotifyTG:          notifyTG,
		IdempotencyKey:    uuid.New(),
	})
	require.NoError(t, err)
	return runID
}

// --- хелперы входящих updates -------------------------------------------------

func (f *tgFixture) nextUpdateID() int64 {
	f.updateSeq++
	return f.updateSeq
}

// pushMessage — входящее сообщение от chatID; replyToID != 0 — reply.
func (f *tgFixture) pushMessage(chatID int64, text string, replyToID int64) Update {
	upd := Update{
		UpdateID: f.nextUpdateID(),
		Message: &Message{
			MessageID: 5000 + f.updateSeq,
			Chat:      Chat{ID: chatID},
			Text:      text,
		},
	}
	if replyToID != 0 {
		upd.Message.ReplyToMessage = &Message{MessageID: replyToID, Chat: Chat{ID: chatID}}
	}
	f.bot.updates <- upd
	return upd
}

func (f *tgFixture) pushCallback(chatID int64, callbackID, data string) {
	f.bot.updates <- Update{
		UpdateID: f.nextUpdateID(),
		CallbackQuery: &CallbackQuery{
			ID:      callbackID,
			Data:    data,
			Message: &Message{MessageID: 9000 + f.updateSeq, Chat: Chat{ID: chatID}},
		},
	}
}

// waitSent ждёт отправленное сообщение с подстрокой в тексте.
func (f *tgFixture) waitSent(t *testing.T, substr string) sentMessage {
	t.Helper()
	var found sentMessage
	require.Eventually(t, func() bool {
		for _, sm := range f.bot.snapshot() {
			if strings.Contains(sm.req.Text, substr) {
				found = sm
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "message containing %q must be sent", substr)
	return found
}

func (f *tgFixture) countSent(substr string) int {
	n := 0
	for _, sm := range f.bot.snapshot() {
		if strings.Contains(sm.req.Text, substr) {
			n++
		}
	}
	return n
}

// --- acceptance: привязка -----------------------------------------------------

func TestPairingFlow(t *testing.T) {
	f := newTgFixture(t, false)
	f.start(t)
	ctx := context.Background()

	// /start от чужого чата → код привязки
	f.pushMessage(777, "/start", 0)
	msg := f.waitSent(t, "Код привязки:")
	assert.Equal(t, int64(777), msg.req.ChatID)

	code := strings.TrimPrefix(strings.Split(msg.req.Text, "\n")[0], "Код привязки: ")

	// неверный код → ошибка валидации
	_, err := f.adapter.PairByCode(ctx, "000000")
	require.Error(t, err)

	// верный код → чат в whitelist
	chatID, err := f.adapter.PairByCode(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, int64(777), chatID)

	allowed, err := f.tg.IsChatAllowed(ctx, 777)
	require.NoError(t, err)
	assert.True(t, allowed)

	// код одноразовый: повтор — ошибка
	_, err = f.adapter.PairByCode(ctx, code)
	require.Error(t, err)

	// /start от привязанного чата
	f.pushMessage(777, "/start", 0)
	f.waitSent(t, "Чат уже привязан")
}

// --- acceptance: вопрос → reply-ответ -----------------------------------------

func TestQuestionGateAnsweredViaReply(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)
	ctx := context.Background()

	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	gate, err := f.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindQuestion,
		Question: "Какой формат ответа?",
	})
	require.NoError(t, err)

	// гейт пришёл в TG с подсказкой про reply
	msg := f.waitSent(t, "Какой формат ответа?")
	assert.Contains(t, msg.req.Text, "Ответьте reply")

	// reply на сообщение гейта → гейт answered
	f.pushMessage(testChatID, "JSON", msg.id)
	f.waitSent(t, "Ответ принят ✅")

	require.Eventually(t, func() bool {
		g, err := f.gates.GetGateByID(ctx, gate.ID)
		return err == nil && g.State == dtorep.GateStateAnswered &&
			g.Answer != nil && *g.Answer == "JSON"
	}, 5*time.Second, 10*time.Millisecond, "gate must be answered via TG reply")
}

// --- acceptance: plan_approval → callback approve + повтор ---------------------

func TestPlanApprovalCallbackApproveAndRepeat(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)
	ctx := context.Background()

	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	gate, err := f.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindPlanApproval,
		Question: "План: 3 шага",
	})
	require.NoError(t, err)

	// сообщение с inline-кнопками
	msg := f.waitSent(t, "План: 3 шага")
	require.NotEmpty(t, msg.req.InlineKeyboard)
	assert.Equal(t, "Approve", msg.req.InlineKeyboard[0][0].Text)
	assert.Equal(t, "Reject", msg.req.InlineKeyboard[0][1].Text)

	// approve → гейт approved
	f.pushCallback(testChatID, "cb-1", "gate:"+gate.ID+":approve")
	require.Eventually(t, func() bool {
		for _, a := range f.bot.answersSnapshot() {
			if a.id == "cb-1" && strings.Contains(a.text, "Принято ✅") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	g, err := f.gates.GetGateByID(ctx, gate.ID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.GateStateApproved, g.State)

	// повторное нажатие → «уже резолвнуто», дублей нет
	f.pushCallback(testChatID, "cb-2", "gate:"+gate.ID+":approve")
	require.Eventually(t, func() bool {
		for _, a := range f.bot.answersSnapshot() {
			if a.id == "cb-2" && strings.Contains(a.text, "уже резолвнут") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	g, err = f.gates.GetGateByID(ctx, gate.ID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.GateStateApproved, g.State)

	evs, err := f.journal.Replay(ctx, dtorep.RunIDAll, 0, 100)
	require.NoError(t, err)
	resolved := 0
	for _, ev := range evs {
		if ev.Kind == runsmachine.EventKindGateResolved {
			resolved++
		}
	}
	assert.Equal(t, 1, resolved, "повторный callback не должен порождать дубль gate.resolved")
}

// --- acceptance: повторная доставка update_id ----------------------------------

func TestDuplicateUpdateIgnored(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)

	// один и тот же update (Telegram ретраит) доставлен дважды
	upd := f.pushMessage(testChatID, "/status", 0)
	f.bot.updates <- upd // повторная доставка

	f.waitSent(t, "Активных ранов нет")
	time.Sleep(200 * time.Millisecond) // даём обработать дубль, если бы он не фильтровался
	assert.Equal(t, 1, f.countSent("Активных ранов нет"),
		"повторный update_id не должен порождать второе действие (D-12)")
}

// --- acceptance: /status и /stop -----------------------------------------------

func TestStatusAndStop(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)
	ctx := context.Background()

	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := f.machine.StartStage(ctx, runID, "plan", "fake")
	require.NoError(t, err)
	require.NoError(t, f.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	// /status — активный ран с проектом и этапом
	f.pushMessage(testChatID, "/status", 0)
	status := f.waitSent(t, "Активные раны")
	assert.Contains(t, status.req.Text, "test-project")
	assert.Contains(t, status.req.Text, "plan")

	// корневое сообщение рана — первое уведомление (✅ по этапу)
	require.NoError(t, f.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))
	root := f.waitSent(t, "✅ plan · iter 1")

	run, err := f.runs.GetRunByID(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, run.TgRootMessageID, "корневое сообщение рана записано в runs")
	assert.Equal(t, root.id, *run.TgRootMessageID)

	// /stop reply на корневое сообщение рана
	f.pushMessage(testChatID, "/stop", root.id)
	f.waitSent(t, "Ран остановлен ⏹")

	run, err = f.runs.GetRunByID(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateStopped, run.State)
}

// --- acceptance: текст-реплай на корень рана → note ----------------------------

func TestNoteViaReplyToRunRoot(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)
	ctx := context.Background()

	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := f.machine.StartStage(ctx, runID, "plan", "fake")
	require.NoError(t, err)
	require.NoError(t, f.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	// прерывание этапа → ⚠️-уведомление = корневое сообщение рана
	require.NoError(t, f.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateInterrupted, dtorep.StageTransitionFields{}))
	root := f.waitSent(t, "⚠️ plan · прерван")

	// текст-реплай на корень (вне гейта) → queue note (D-22)
	f.pushMessage(testChatID, "не забудь про логи", root.id)
	f.waitSent(t, "Заметка поставлена в очередь 📝")

	notes, err := f.notes.ListNotesByRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, "не забудь про логи", notes[0].Text)
	assert.Equal(t, dtorep.NoteKindNote, notes[0].Kind)
}

// --- acceptance: оффлайн-сводка (D-72) ------------------------------------------

func TestOfflineSummaryOnStartup(t *testing.T) {
	f := newTgFixture(t, true) // чат в whitelist, адаптер НЕ запущен
	ctx := context.Background()

	// пока демон «был оффлайн»: события копились в журнале
	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	_, err := f.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindQuestion,
		Question: "Проспанный вопрос?",
	})
	require.NoError(t, err)

	// маркер доставки отстаёт (0 = ничего не доставлено)
	latest, err := f.journal.LatestID(ctx)
	require.NoError(t, err)
	require.Greater(t, latest, int64(0))

	// старт адаптера → ровно ОДНО сводное сообщение (не лента событий)
	f.start(t)
	summary := f.waitSent(t, "💤 За время простоя")
	assert.Contains(t, summary.req.Text, "открыто гейтов: 1")

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, len(f.bot.snapshot()), "только сводка, без ленты пропущенных событий")

	// маркер подтянут до актуального id
	raw, err := f.tg.GetState(ctx, stateKeyLastDeliveredEvt)
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatInt(latest, 10), raw)
}

// --- acceptance: маркер сводки не двигается при фейле send (F-06) ---------------

func TestOfflineSummaryMarkerNotMovedOnSendFailure(t *testing.T) {
	f := newTgFixture(t, true) // чат в whitelist, адаптер НЕ запущен
	ctx := context.Background()

	// накопились события за «простой»
	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	latest, err := f.journal.LatestID(ctx)
	require.NoError(t, err)

	// send падает → syncMissed ошибка, маркер не сдвинут
	f.bot.setSendErr(errors.New("network down"))
	require.Error(t, f.adapter.syncMissed(ctx))
	assert.Equal(t, int64(0), f.adapter.loadInt64State(ctx, stateKeyLastDeliveredEvt),
		"маркер не должен двигаться до успешной отправки сводки")
	assert.Empty(t, f.bot.snapshot())

	// сеть восстановилась → сводка уходит, маркер подтягивается
	f.bot.setSendErr(nil)
	require.NoError(t, f.adapter.syncMissed(ctx))
	assert.Equal(t, latest, f.adapter.loadInt64State(ctx, stateKeyLastDeliveredEvt))
	assert.Equal(t, 1, f.countSent("💤 За время простоя"))
}

// --- acceptance: live-путь — маркер не двигается при фейле send (fix-task-3 п.2) --

func TestLiveMarkerNotMovedOnSendFailure(t *testing.T) {
	f := newTgFixture(t, true) // чат в whitelist, адаптер НЕ запущен
	ctx := context.Background()

	// живое событие, маппящееся в сообщение (gate.opened → ❓)
	runID := seedRun(t, f.db, true)
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	_, err := f.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindQuestion,
		Question: "Live-вопрос?",
	})
	require.NoError(t, err)

	evs, err := f.journal.Replay(ctx, dtorep.RunIDAll, 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	ev := evs[len(evs)-1] // gate.opened

	// событие без маппинга (run.state_changed) — «учтено»: маркер двигается
	// даже при упавшей сети
	f.bot.setSendErr(errors.New("network down"))
	require.True(t, f.adapter.handleEvent(ctx, evs[0]))
	assert.Equal(t, evs[0].ID, f.adapter.loadInt64State(ctx, stateKeyLastDeliveredEvt))

	// send падает → handleEvent=false, маркер не сдвинут, сообщений нет
	require.False(t, f.adapter.handleEvent(ctx, ev))
	assert.Equal(t, evs[0].ID, f.adapter.loadInt64State(ctx, stateKeyLastDeliveredEvt),
		"маркер не должен двигаться до успешной live-отправки")
	assert.Empty(t, f.bot.snapshot())

	// сеть восстановилась → событие уходит, маркер двигается
	f.bot.setSendErr(nil)
	require.True(t, f.adapter.handleEvent(ctx, ev))
	assert.Equal(t, ev.ID, f.adapter.loadInt64State(ctx, stateKeyLastDeliveredEvt))
	assert.Equal(t, 1, f.countSent("Live-вопрос?"))
}

// --- acceptance: notify_tg=false → ран молчит (D-73) ----------------------------

func TestMutedRunIsSilent(t *testing.T) {
	f := newTgFixture(t, true)
	f.start(t)
	ctx := context.Background()

	runID := seedRun(t, f.db, false) // notify_tg=false
	require.NoError(t, f.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	_, err := f.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindQuestion,
		Question: "Тихий вопрос?",
	})
	require.NoError(t, err)

	time.Sleep(300 * time.Millisecond) // даём sender'у обработать события
	assert.Empty(t, f.bot.snapshot(), "ран с notify_tg=false не должен слать сообщения (D-73)")
}

// --- unit: маппинг gate.opened --------------------------------------------------

func TestMapGateOpened(t *testing.T) {
	a := &Adapter{}
	mkEvent := func(kind dtorep.GateKind, question, contextJSON string) dtorep.Event {
		return dtorep.Event{
			Kind: runsmachine.EventKindGateOpened,
			PayloadJSON: `{"ID":"g1","RunID":"r1","Kind":"` + string(kind) +
				`","Question":` + strconv.Quote(question) +
				`,"ContextJSON":` + strconv.Quote(contextJSON) + `}`,
		}
	}

	// question → reply-подсказка, без кнопок
	text, kb, gateID := a.mapGateOpened(mkEvent(dtorep.GateKindQuestion, "Что делать?", ""))
	assert.Contains(t, text, "Что делать?")
	assert.Contains(t, text, "Ответьте reply")
	assert.Empty(t, kb)
	assert.Equal(t, "g1", gateID)

	// plan_approval → Approve/Reject
	text, kb, _ = a.mapGateOpened(mkEvent(dtorep.GateKindPlanApproval, "План", ""))
	require.Len(t, kb, 1)
	assert.Equal(t, "Approve", kb[0][0].Text)
	assert.Equal(t, "gate:g1:approve", kb[0][0].CallbackData)
	assert.Equal(t, "gate:g1:reject", kb[0][1].CallbackData)
	assert.Contains(t, text, "План")

	// final_review → Принять/Отклонить
	_, kb, _ = a.mapGateOpened(mkEvent(dtorep.GateKindFinalReview, "Принять?", ""))
	require.Len(t, kb, 1)
	assert.Equal(t, "Принять", kb[0][0].Text)
	assert.Equal(t, "Отклонить", kb[0][1].Text)

	// escalation → findings из context_json + reply-подсказка
	text, kb, _ = a.mapGateOpened(mkEvent(dtorep.GateKindEscalation, "Петля застряла",
		`{"findings":["REV-001: нет тестов"]}`))
	assert.Contains(t, text, "REV-001")
	assert.Contains(t, text, "Ответьте reply")
	assert.Empty(t, kb)
}
