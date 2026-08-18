// Command acp-spike — research-спайк T-27 (эксперимент, M4).
//
// Проверяет альтернативу модели «вопросы как артефакт» (D-20): живой
// двусторонний протокол ACP (Agent Client Protocol) поверх stdio.
// Программа самодостаточна: запускает `kimi acp` как child-процесс,
// выполняет handshake, создаёт сессию, отправляет ОДИН дешёвый промпт,
// логирует стрим session/update и корректно завершается.
// В ядро/supervisor НЕ встраивается. Только stdlib.
//
// Запуск: cd backend && go run ./cmd/acp-spike
//
// ФАКТЫ ПРОТОКОЛА ACP (по https://agentclientprotocol.com, spec v1):
//
//  1. Фрейминг — NDJSON, а НЕ LSP-стиль: каждое JSON-RPC 2.0 сообщение —
//     одна строка, разделитель "\n", встроенные переводы строк внутри
//     сообщения ЗАПРЕЩЕНЫ. Никаких заголовков Content-Length (это LSP/MCP-
//     старый стиль; ACP их не использует). См. protocol/v1/transports:
//     "Messages are delimited by newlines (\n), and MUST NOT contain
//     embedded newlines."
//  2. Корреляция — по JSON-RPC "id" (у нас монотонные int). Уведомления
//     (notifications, напр. session/update) id не имеют и ответа не ждут.
//  3. Жизненный цикл: initialize (protocolVersion + capabilities) →
//     session/new (cwd, mcpServers) → session/prompt → стрим session/update
//     → ответ на session/prompt со stopReason (end_turn|max_tokens|
//     max_turn_requests|refusal|cancelled).
//  4. Двусторонность: АГЕНТ тоже шлёт клиенту запросы —
//     session/request_permission (разрешение на tool call), fs/*, terminal/*.
//     Клиент ОБЯЗАН отвечать на все входящие запросы, иначе ход повиснет.
//  5. Отмена: session/cancel — уведомление; агент обязан ответить на
//     pending session/prompt со stopReason="cancelled" (не ошибкой).
//  6. Идемпотентность: в протоколе НЕТ idempotency-ключей. session/new
//     НЕ идемпотентен — каждый вызов создаёт новую сессию с новым id.
//     Повторная отправка prompt с тем же JSON-RPC id не определена —
//     дедупликация повторов ложится на клиента.
//  7. Обрыв процесса: живой ACP-процесс НЕ переживает рестарт клиента
//     (демона) — stdio-пайпы умирают вместе с родителем. История сессии
//     у kimi сохраняется на диск (~/.kimi-code/sessions/...), но вернуться
//     в неё можно только из НОВОГО процесса через session/load (реплей
//     истории нотификациями session/update; требует capability loadSession)
//     или session/resume (без реплея; требует sessionCapabilities.resume).
//     Для glamor это означает конфликт с D-15: startup recovery видит
//     running-стадию-сироту, а "живого диалога" уже нет.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// promptText — единственный разрешённый LLM-вызов спайка (T-27: один
// дешёвый промпт, больше не запускать).
const promptText = "Ответь одним словом: ок"

const (
	// protocolVersion — MAJOR-версия ACP по спеке v1. Агент обязан ответить
	// той же версией, либо последней поддерживаемой; при несовместимости
	// клиент должен закрыть соединение.
	protocolVersion = 1
	// overallTimeout — внешний таймаут на весь эксперимент: по опыту
	// harness-матрицы CLI-агенты могут жить в retry-лупе минутами.
	overallTimeout = 180 * time.Second
	// maxLineBytes — защитный лимит строки NDJSON (чанки текста, diff'ы).
	maxLineBytes = 4 << 20
)

// rpcMessage — конверт JSON-RPC 2.0: и запрос, и ответ, и уведомление.
// Поля Result/Error/ID — RawMessage, чтобы разбирать по месту.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// acpClient — минимальный ACP-клиент поверх stdio-пайпов child-процесса.
type acpClient struct {
	stdin io.WriteCloser

	writeMu sync.Mutex // сериализация записи в stdin
	nextID  int

	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage // id (как сырые JSON-байты) → канал ответа
}

func newACPClient(stdin io.WriteCloser) *acpClient {
	return &acpClient{stdin: stdin, pending: make(map[string]chan rpcMessage)}
}

// send посылает JSON-RPC запрос и возвращает канал, куда придёт ответ.
func (c *acpClient) send(method string, params any) (<-chan rpcMessage, error) {
	c.writeMu.Lock()
	c.nextID++
	id := c.nextID
	c.writeMu.Unlock()

	idRaw, _ := json.Marshal(id)
	ch := make(chan rpcMessage, 1)
	c.pendingMu.Lock()
	c.pending[string(idRaw)] = ch
	c.pendingMu.Unlock()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.write(msg, ">>"); err != nil {
		return nil, err
	}
	return ch, nil
}

// write сериализует сообщение в одну строку NDJSON и пишет в stdin агента.
// Фрейминг ACP — строго "\n"-разделённый JSON, без Content-Length.
func (c *acpClient) write(v any, prefix string) error {
	data, err := json.Marshal(v) // json.Marshal не вставляет сырых \n вне строк
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	log.Printf("%s %s", prefix, truncate(string(data), 600))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// route разбирает входящую строку от агента: ответ (есть id + result/error),
// запрос от агента (method + id) или уведомление (method без id).
func (c *acpClient) route(msg rpcMessage) {
	switch {
	case msg.Method == "":
		// Ответ на наш запрос — коррелируем по id.
		c.pendingMu.Lock()
		ch, ok := c.pending[string(msg.ID)]
		delete(c.pending, string(msg.ID))
		c.pendingMu.Unlock()
		if !ok {
			log.Printf("!! ответ с неизвестным id=%s (гонка или дубликат)", string(msg.ID))
			return
		}
		ch <- msg
	case msg.ID != nil:
		// Запрос АГЕНТА клиенту — обязаны ответить, иначе ход зависнет.
		c.handleAgentRequest(msg)
	default:
		// Уведомление (session/update и пр.) — только логируем.
		logUpdate(msg)
	}
}

// handleAgentRequest отвечает на двусторонние вызовы агента.
// Спайк не реализует fs/terminal — отвечаем JSON-RPC ошибкой -32601;
// на запрос пермишена выбираем первый "allow"-вариант (auto-approve по
// аналогии с headless-режимом `kimi -p`, где политика всегда auto).
func (c *acpClient) handleAgentRequest(msg rpcMessage) {
	switch msg.Method {
	case "session/request_permission":
		var p struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Name     string `json:"name"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		chosen := ""
		for _, o := range p.Options {
			if chosen == "" || strings.HasPrefix(o.Kind, "allow") {
				chosen = o.OptionID
				if strings.HasPrefix(o.Kind, "allow") {
					break
				}
			}
		}
		if chosen == "" {
			// Нет вариантов — отменяем запрос пермишена по спеке.
			err := c.write(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
			}, "<<<")
			if err != nil {
				log.Printf("!! ответ на request_permission: %v", err)
			}
			return
		}
		log.Printf("<< agent-request %s: авто-выбор optionId=%q", msg.Method, chosen)
		err := c.write(map[string]any{
			"jsonrpc": "2.0", "id": msg.ID,
			"result": map[string]any{"outcome": map[string]any{
				"outcome": "selected", "optionId": chosen,
			}},
		}, "<<<")
		if err != nil {
			log.Printf("!! ответ на request_permission: %v", err)
		}
	default:
		// fs/read_text_file, fs/write_text_file, terminal/* — спайк не
		// объявлял эти capabilities в initialize, но на всякий случай
		// отвечаем стандартной ошибкой "method not found".
		log.Printf("<< agent-request %s: не поддерживается, отвечаем -32601", msg.Method)
		err := c.write(map[string]any{
			"jsonrpc": "2.0", "id": msg.ID,
			"error": map[string]any{"code": -32601, "message": "acp-spike: метод не реализован"},
		}, "<<<")
		if err != nil {
			log.Printf("!! ответ на %s: %v", msg.Method, err)
		}
	}
}

// logUpdate логирует session/update: тип обновления + срез текста.
func logUpdate(msg rpcMessage) {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
			Status string `json:"status"`
			Title  string `json:"title"`
		} `json:"update"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	kind := p.Update.SessionUpdate
	if kind == "" {
		kind = msg.Method
	}
	extra := p.Update.Content.Text
	if extra == "" {
		extra = strings.TrimSpace(p.Update.Status + " " + p.Update.Title)
	}
	log.Printf("<< notify %s [%s] %s", msg.Method, kind, truncate(extra, 200))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// waitResponse ждёт ответ на запрос с таймаутом и возвращает result.
func waitResponse(ctx context.Context, ch <-chan rpcMessage, method string) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: RPC error %d: %s", method, msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.SetPrefix("[acp-spike] ")

	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout)
	defer cancel()

	t0 := time.Now()

	// --- Запуск child-процесса `kimi acp` (ACP server over stdio) ---
	// CommandContext убьёт процесс по отмене контекста (таймаут/выход).
	cmd := exec.CommandContext(ctx, "kimi", "acp")
	stdin, err := cmd.StdinPipe()
	must(err, "stdin pipe")
	stdout, err := cmd.StdoutPipe()
	must(err, "stdout pipe")
	stderr, err := cmd.StderrPipe()
	must(err, "stderr pipe")

	if err := cmd.Start(); err != nil {
		log.Printf("ИТОГ: FAIL — `kimi acp` не стартовал: %v", err)
		//nolint:gocritic // exitAfterDefer: спайк-утилита, cancel не критичен
		os.Exit(1)
	}
	log.Printf("kimi acp запущен, pid=%d", cmd.Process.Pid)

	// stderr агента — отдельный канал диагностики (по матрице harness'ов
	// kimi пишет thinking/нотисы в stderr; в NDJSON-канал они не попадают).
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
		for sc.Scan() {
			log.Printf("[kimi stderr] %s", truncate(sc.Text(), 300))
		}
	}()

	client := newACPClient(stdin)

	// Читатель stdout: NDJSON — одна строка = одно JSON-RPC сообщение.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				// Парсер обязан пропускать невалидные строки, а не падать
				// (по матрице: в stdout CLI-агентов вклинивается мусор).
				log.Printf("!! не-JSON строка в stdout (пропущена): %s", truncate(string(line), 200))
				continue
			}
			client.route(msg)
		}
		if err := sc.Err(); err != nil {
			log.Printf("!! stdout scanner: %v", err)
		}
	}()

	// --- Шаг 1: initialize (protocol version + client capabilities) ---
	// clientCapabilities минимальны: fs/terminal НЕ объявляем — агент не
	// должен звать fs/* и terminal/* (опущенная capability = unsupported).
	initCh, err := client.send("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "glamor-acp-spike",
			"title":   "glamor ACP spike (T-27)",
			"version": "0.0.1",
		},
	})
	must(err, "send initialize")
	initRes, err := waitResponse(ctx, initCh, "initialize")
	if err != nil {
		finishFail(cmd, cancel, "initialize", err)
	}
	tInit := time.Since(t0)

	var initInfo struct {
		ProtocolVersion int             `json:"protocolVersion"`
		AgentInfo       map[string]any  `json:"agentInfo"`
		AgentCaps       json.RawMessage `json:"agentCapabilities"`
		AuthMethods     []any           `json:"authMethods"`
	}
	_ = json.Unmarshal(initRes, &initInfo)
	log.Printf("initialize OK за %v: protocolVersion=%d agentInfo=%v authMethods=%d capabilities=%s",
		tInit.Round(time.Millisecond), initInfo.ProtocolVersion, initInfo.AgentInfo,
		len(initInfo.AuthMethods), truncate(string(initInfo.AgentCaps), 400))
	if initInfo.ProtocolVersion != protocolVersion {
		finishFail(cmd, cancel, "initialize",
			fmt.Errorf("несовместимая версия протокола: %d != %d", initInfo.ProtocolVersion, protocolVersion))
	}

	// --- Шаг 2: session/new (cwd + пустой список MCP-серверов) ---
	// ВНИМАНИЕ: метод НЕ идемпотентен — каждый вызов = новая сессия.
	cwd, err := os.Getwd()
	must(err, "getwd")
	t1 := time.Now()
	newCh, err := client.send("session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	must(err, "send session/new")
	newRes, err := waitResponse(ctx, newCh, "session/new")
	if err != nil {
		finishFail(cmd, cancel, "session/new", err)
	}
	var newInfo struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(newRes, &newInfo)
	if newInfo.SessionID == "" {
		finishFail(cmd, cancel, "session/new", errors.New("пустой sessionId в ответе"))
	}
	tNew := time.Since(t1)
	log.Printf("session/new OK за %v: sessionId=%s (cwd=%s)", tNew.Round(time.Millisecond), newInfo.SessionID, cwd)

	// --- Шаг 3: session/prompt — ЕДИНСТВЕННЫЙ LLM-вызов спайка ---
	t2 := time.Now()
	promptCh, err := client.send("session/prompt", map[string]any{
		"sessionId": newInfo.SessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": promptText},
		},
	})
	must(err, "send session/prompt")

	// Время до первого session/update (TTFT-аналог) видно по меткам
	// времени общего лога; здесь ждём финальный ответ со stopReason.
	promptRes, err := waitResponse(ctx, promptCh, "session/prompt")
	if err != nil {
		finishFail(cmd, cancel, "session/prompt", err)
	}
	tPrompt := time.Since(t2)
	var stopInfo struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(promptRes, &stopInfo)

	log.Printf("session/prompt OK за %v: stopReason=%q", tPrompt.Round(time.Millisecond), stopInfo.StopReason)

	// --- Шаг 4: корректное завершение ---
	// Закрываем stdin (EOF) и даём процессу завершиться; по таймауту —
	// kill через отмену контекста. Живой процесс ACP «повисших» детей
	// оставлять нельзя: демон glamor должен убирать за собой.
	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case werr := <-waitDone:
		log.Printf("kimi acp завершился: %v", werr)
	case <-time.After(5 * time.Second):
		cancel()
		<-waitDone
		log.Printf("kimi acp убит по таймауту завершения")
	}

	log.Printf("=== ИТОГ СПАЙКА: SUCCESS ===")
	log.Printf("handshake(initialize): %v", tInit.Round(time.Millisecond))
	log.Printf("session/new:           %v", tNew.Round(time.Millisecond))
	log.Printf("prompt (1 LLM-вызов):  %v", tPrompt.Round(time.Millisecond))
	log.Printf("итого:                 %v", time.Since(t0).Round(time.Millisecond))
	log.Printf("sessionId=%s stopReason=%s", newInfo.SessionID, stopInfo.StopReason)
}

// finishFail фиксирует провал шага как результат эксперимента (это тоже
// данные по T-27) и завершает child-процесс.
func finishFail(cmd *exec.Cmd, cancel context.CancelFunc, step string, err error) {
	log.Printf("=== ИТОГ СПАЙКА: FAIL на шаге %s: %v ===", step, err)
	cancel()
	_ = cmd.Wait()
	os.Exit(1)
}

func must(err error, what string) {
	if err != nil {
		log.Fatalf("фатально: %s: %v", what, err)
	}
}
