package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/service/runsapi"
)

// --- poller: TG → ядро --------------------------------------------------------

// pollerLoop — long polling getUpdates. update_id — idempotency-key команд
// (D-12): last_update_id персистится в tg_state, повторный update
// игнорируется.
func (a *Adapter) pollerLoop(ctx context.Context) {
	offset := a.loadInt64State(ctx, stateKeyLastUpdateID) + 1

	for {
		if ctx.Err() != nil {
			return
		}

		updates, err := a.client.GetUpdates(ctx, offset, a.cfg.PollTimeoutSec)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "tg: getUpdates failed, retrying",
				slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.cfg.PollRetryDelay):
				continue
			}
		}

		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			// D-12: повторная доставка (Telegram ретраит) — игнорируем
			if upd.UpdateID <= a.loadInt64State(ctx, stateKeyLastUpdateID) {
				continue
			}

			a.handleUpdate(ctx, upd)

			if err := a.tg.SetState(ctx, stateKeyLastUpdateID, strconv.FormatInt(upd.UpdateID, 10)); err != nil {
				slog.WarnContext(ctx, "tg: failed to persist last_update_id",
					slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
			}
		}
	}
}

// handleUpdate маршрутизирует update: сообщение или callback_query.
func (a *Adapter) handleUpdate(ctx context.Context, upd Update) {
	switch {
	case upd.Message != nil:
		a.handleMessage(ctx, upd.Message, upd.UpdateID)
	case upd.CallbackQuery != nil:
		a.handleCallback(ctx, upd.CallbackQuery)
	}
}

// handleMessage — входящее сообщение. Чужие чаты (не в tg_chats)
// игнорируются, кроме /start (привязка по коду).
func (a *Adapter) handleMessage(ctx context.Context, msg *Message, updateID int64) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	allowed, err := a.tg.IsChatAllowed(ctx, chatID)
	if err != nil {
		slog.WarnContext(ctx, "tg: failed to check chat whitelist",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		return
	}

	if !allowed {
		if strings.HasPrefix(text, "/start") {
			a.startPairing(ctx, chatID)
		}
		return
	}

	switch {
	case strings.HasPrefix(text, "/start"):
		a.reply(ctx, chatID, msg.MessageID, "Чат уже привязан ✅")
	case strings.HasPrefix(text, "/status"):
		a.reply(ctx, chatID, msg.MessageID, a.formatStatus(ctx))
	case strings.HasPrefix(text, "/stop"):
		a.handleStop(ctx, msg)
	case msg.ReplyToMessage != nil && text != "":
		a.handleReply(ctx, msg, updateID)
	default:
		// свободный текст без reply — не команда, игнорируем
	}
}

// startPairing — /start от чужого чата: генерируем 6-значный код и
// запоминаем (chat_id, code) в tg_state; привязку завершает REST
// POST /telegram/pair из UI.
func (a *Adapter) startPairing(ctx context.Context, chatID int64) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		slog.ErrorContext(ctx, "tg: failed to generate pair code",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		return
	}
	code := strconv.FormatInt(n.Int64()+100000, 10)

	if err := a.tg.SetState(ctx, stateKeyPendingPairCode, code); err != nil {
		slog.ErrorContext(ctx, "tg: failed to store pair code",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		return
	}
	if err := a.tg.SetState(ctx, stateKeyPendingPairChat, strconv.FormatInt(chatID, 10)); err != nil {
		slog.ErrorContext(ctx, "tg: failed to store pair chat",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		return
	}

	if _, err := a.send(ctx, SendMessageRequest{
		ChatID: chatID,
		Text: "Код привязки: " + code +
			"\n\nВведите его в настройках glamor, чтобы привязать этот чат.",
	}); err != nil {
		slog.WarnContext(ctx, "tg: failed to send pair code",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
}

// PairByCode — завершение привязки из UI (REST POST /telegram/pair):
// код из /start → chat_id в whitelist. Неверный/протухший код —
// ErrValidation (400 на транспорте).
func (a *Adapter) PairByCode(ctx context.Context, code string) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, fmt.Errorf("pair code must be 6 digits: %w", cstmerrors.ErrValidation)
	}

	pending, err := a.tg.GetState(ctx, stateKeyPendingPairCode)
	if err != nil || pending == "" || pending != code {
		return 0, fmt.Errorf("pair code not found or expired: %w", cstmerrors.ErrValidation)
	}

	chatRaw, err := a.tg.GetState(ctx, stateKeyPendingPairChat)
	if err != nil {
		return 0, fmt.Errorf("pending pair chat is missing: %w", cstmerrors.ErrValidation)
	}
	chatID, err := strconv.ParseInt(chatRaw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pending pair chat is invalid: %w", cstmerrors.ErrValidation)
	}

	if err := a.tg.AddChat(ctx, chatID); err != nil {
		return 0, fmt.Errorf("failed to add chat to whitelist: %w", err)
	}
	// одноразовый код — погашаем
	if err := a.tg.DeleteState(ctx, stateKeyPendingPairCode); err != nil {
		slog.WarnContext(ctx, "tg: failed to clear pair code",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
	if err := a.tg.DeleteState(ctx, stateKeyPendingPairChat); err != nil {
		slog.WarnContext(ctx, "tg: failed to clear pair chat",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
	return chatID, nil
}

// handleReply — reply на сообщение бота от привязанного чата:
// reply на сообщение гейта → ответ на гейт; reply на корневое сообщение
// рана → queue note (D-22).
func (a *Adapter) handleReply(ctx context.Context, msg *Message, updateID int64) {
	replyToID := msg.ReplyToMessage.MessageID

	// reply на сообщение гейта?
	gm, err := a.tg.GetGateMessageByMessageID(ctx, msg.Chat.ID, replyToID)
	switch {
	case err == nil:
		a.resolveGateByReply(ctx, msg, gm.GateID)
		return
	case !errors.Is(err, cstmerrors.ErrNotFound):
		slog.WarnContext(ctx, "tg: failed to lookup gate message",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		return
	}

	// reply на корневое сообщение рана → queue note (D-22)
	run, err := a.catalog.GetRunByTgRootMessageID(ctx, replyToID)
	if err != nil {
		if !errors.Is(err, cstmerrors.ErrNotFound) {
			slog.WarnContext(ctx, "tg: failed to lookup run by root message",
				slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		}
		return // reply на неизвестное сообщение — игнорируем
	}

	// idempotency-key из update_id (D-12): повторная доставка не дублирует
	if _, err := a.runsSvc.CreateNote(ctx, run.ID, msg.Text, "tg-"+strconv.FormatInt(updateID, 10)); err != nil {
		slog.WarnContext(ctx, "tg: failed to create note",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "⚠️ Не удалось поставить заметку.")
		return
	}
	a.reply(ctx, msg.Chat.ID, msg.MessageID, "Заметка поставлена в очередь 📝")
}

// resolveGateByReply — текстовый ответ на сообщение гейта. answer/comment
// в ядре идентичны (answered + диалоговый ре-вход этапа, D-20/22), поэтому
// всегда GateActionAnswer.
func (a *Adapter) resolveGateByReply(ctx context.Context, msg *Message, gateID string) {
	_, already, err := a.runsSvc.ResolveGateAPI(ctx, gateID, runsapi.GateActionAnswer, &msg.Text, nil)
	switch {
	case err == nil && already:
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Гейт уже резолвнут.")
	case err == nil:
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Ответ принят ✅")
	case errors.Is(err, cstmerrors.ErrGateAlreadyResolved):
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Гейт уже резолвнут другим решением.")
	case errors.Is(err, cstmerrors.ErrNotFound):
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Гейт не найден.")
	default:
		slog.WarnContext(ctx, "tg: failed to resolve gate",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "⚠️ Не удалось резолвнуть гейт.")
	}
}

// handleCallback — нажатие inline-кнопки: gate:<id>:approve|reject.
// Повторное нажатие → «уже резолвнуто» (идемпотентность ядра, D-12).
func (a *Adapter) handleCallback(ctx context.Context, cq *CallbackQuery) {
	if cq.Message == nil {
		return
	}
	chatID := cq.Message.Chat.ID

	allowed, err := a.tg.IsChatAllowed(ctx, chatID)
	if err != nil || !allowed {
		return // чужой чат — молча игнорируем
	}

	parts := strings.Split(cq.Data, ":")
	if len(parts) != 3 || parts[0] != "gate" {
		a.answerCallback(ctx, cq.ID, "Неизвестное действие.")
		return
	}
	gateID := parts[1]

	var action runsapi.GateAction
	switch parts[2] {
	case "approve":
		action = runsapi.GateActionApprove
	case "reject":
		action = runsapi.GateActionReject
	default:
		a.answerCallback(ctx, cq.ID, "Неизвестное действие.")
		return
	}

	_, already, err := a.runsSvc.ResolveGateAPI(ctx, gateID, action, nil, nil)
	switch {
	case err == nil && already:
		a.answerCallback(ctx, cq.ID, "Гейт уже резолвнут.")
	case err == nil:
		if action == runsapi.GateActionApprove {
			a.answerCallback(ctx, cq.ID, "Принято ✅")
		} else {
			a.answerCallback(ctx, cq.ID, "Отклонено ❌")
		}
	case errors.Is(err, cstmerrors.ErrGateAlreadyResolved):
		a.answerCallback(ctx, cq.ID, "Гейт уже резолвнут другим решением.")
	default:
		slog.WarnContext(ctx, "tg: failed to resolve gate via callback",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		a.answerCallback(ctx, cq.ID, "⚠️ Ошибка, попробуйте позже.")
	}
}

// handleStop — /stop reply на сообщение рана (T-19): ран находится по
// корневому сообщению или по сообщению гейта.
func (a *Adapter) handleStop(ctx context.Context, msg *Message) {
	if msg.ReplyToMessage == nil {
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Отправьте /stop reply'ем на сообщение рана.")
		return
	}
	replyToID := msg.ReplyToMessage.MessageID

	runID := ""
	if run, err := a.catalog.GetRunByTgRootMessageID(ctx, replyToID); err == nil {
		runID = run.ID
	} else if gm, err := a.tg.GetGateMessageByMessageID(ctx, msg.Chat.ID, replyToID); err == nil {
		if gate, err := a.catalog.GetGate(ctx, gm.GateID); err == nil {
			runID = gate.RunID
		}
	}
	if runID == "" {
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "Ран по этому сообщению не найден.")
		return
	}

	if _, err := a.runsSvc.StopRun(ctx, runID); err != nil {
		slog.WarnContext(ctx, "tg: failed to stop run",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		a.reply(ctx, msg.Chat.ID, msg.MessageID, "⚠️ Не удалось остановить ран.")
		return
	}
	a.reply(ctx, msg.Chat.ID, msg.MessageID, "Ран остановлен ⏹")
}

// reply — короткое подтверждение reply'ем на сообщение пользователя.
func (a *Adapter) reply(ctx context.Context, chatID, replyToMessageID int64, text string) {
	if _, err := a.send(ctx, SendMessageRequest{
		ChatID:           chatID,
		Text:             text,
		ReplyToMessageID: replyToMessageID,
	}); err != nil {
		slog.WarnContext(ctx, "tg: failed to send reply",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
}

// answerCallback — ответ на callback_query (ошибки не критичны: «часики»
// просто повисят).
func (a *Adapter) answerCallback(ctx context.Context, id, text string) {
	if err := a.client.AnswerCallbackQuery(ctx, id, text); err != nil {
		slog.WarnContext(ctx, "tg: failed to answer callback",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
}
