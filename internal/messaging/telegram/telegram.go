// Package telegram adapts the Bot API to Tend's durable communication ledger.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const downloadLimit = 20_000_000
const uploadLimit = 50_000_000

// Client owns transport only. All authority and destination snapshots live in app.
type Client struct {
	app         *app.App
	sdk         *bot.Bot
	http        *http.Client
	base, token string
	botID       int64
	log         *slog.Logger
	pollWait    time.Duration
}

// New does not contact Telegram. A custom URL/client supports offline tests.
func New(a *app.App, token string, client *http.Client, base string, log *slog.Logger) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram token is empty")
	}
	if base == "" {
		base = "https://api.telegram.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client = &copyClient
	sdk, err := bot.New(token, bot.WithSkipGetMe(), bot.WithHTTPClient(40*time.Second, client), bot.WithServerURL(base))
	if err != nil {
		return nil, errors.New("telegram client configuration failed")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{app: a, sdk: sdk, http: client, base: strings.TrimSuffix(base, "/"), token: token, log: log, pollWait: time.Second}, nil
}

func (c *Client) Prepare(ctx context.Context) error {
	me, err := c.sdk.GetMe(ctx)
	if err != nil {
		return errors.New("telegram bot identity could not be verified")
	}
	info, err := c.sdk.GetWebhookInfo(ctx)
	if err != nil {
		return errors.New("telegram webhook configuration could not be verified")
	}
	if info.URL != "" {
		return errors.New("telegram webhook is configured; long polling requires an explicit webhook removal")
	}
	c.botID = me.ID
	if err := c.app.ConfigureTelegram(ctx, c.botID); err != nil {
		return err
	}
	return c.app.ObserveTelegramHealth(ctx, "ready")
}

// Run independently receives updates and consumes the durable inbox. Confirming
// a batch never waits on media downloads or model execution.
func (c *Client) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { c.consume(ctx) })
	defer wg.Wait()
	for ctx.Err() == nil {
		err := c.poll(ctx)
		if ctx.Err() == nil {
			health := "ready"
			if err != nil {
				health = "poll_failed"
			}
			_ = c.app.ObserveTelegramHealth(ctx, health)
		}
		if err != nil && ctx.Err() == nil {
			c.log.WarnContext(ctx, "Telegram polling unavailable", "reason", "poll_failed")
		}
		delay := c.pollWait
		var rate callError
		if errors.As(err, &rate) && rate.code == 429 {
			delay = time.Duration(max(rate.retry, 1)) * time.Second
		}
		if err != nil && !wait(ctx, delay) {
			return
		}
	}
}
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type apiResult struct {
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	ErrorCode  int             `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type callError struct {
	code  int
	retry int
}

func (e callError) Error() string { return "Telegram API request failed" }
func (c *Client) request(ctx context.Context, method, contentType string, body io.Reader) (apiResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/bot"+c.token+"/"+method, body)
	if err != nil {
		return apiResult{}, errors.New("telegram request could not be constructed")
	}
	req.Header.Set("Content-Type", contentType)
	response, err := c.http.Do(req)
	if err != nil {
		return apiResult{}, errors.New("telegram transport outcome unknown")
	}
	defer response.Body.Close()
	var result apiResult
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return result, errors.New("telegram response outcome unknown")
	}
	if !result.OK {
		return result, callError{code: result.ErrorCode, retry: result.Parameters.RetryAfter}
	}
	return result, nil
}
func (c *Client) poll(ctx context.Context) error {
	offset, err := c.app.TelegramOffset(ctx, c.botID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"offset": offset, "timeout": 25, "limit": 50, "allowed_updates": []string{"message", "edited_message", "callback_query"}})
	result, err := c.request(ctx, "getUpdates", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var updates []json.RawMessage
	if err = json.Unmarshal(result.Result, &updates); err != nil {
		return errors.New("telegram update response invalid")
	}
	batch := make([]app.TelegramInbox, 0, len(updates))
	for _, raw := range updates {
		var head struct {
			ID int64 `json:"update_id"`
		}
		if err = json.Unmarshal(raw, &head); err != nil || head.ID <= 0 {
			return errors.New("telegram update identity invalid")
		}
		batch = append(batch, app.TelegramInbox{UpdateID: head.ID, Payload: raw})
	}
	if err := c.app.PersistTelegramBatch(ctx, c.botID, batch); err != nil {
		return err
	}
	// A media download ahead of a callback must not hold the client's spinner.
	for _, raw := range updates {
		var update models.Update
		if json.Unmarshal(raw, &update) == nil && update.CallbackQuery != nil {
			c.ackCallback(ctx, update.CallbackQuery.ID, "")
		}
	}
	return nil
}

func (c *Client) consume(ctx context.Context) {
	for ctx.Err() == nil {
		u, ok, err := c.app.PendingTelegram(ctx, c.botID)
		if err != nil || !ok {
			if !wait(ctx, c.pollWait) {
				return
			}
			continue
		}
		if err = c.process(ctx, u); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = c.app.ObserveTelegramHealth(ctx, "input_prepare_failed")
			c.log.WarnContext(ctx, "Telegram input waiting", "reason", "input_prepare_failed", "update_id", u.UpdateID)
			delay := 5 * time.Second
			var rate callError
			if errors.As(err, &rate) && rate.code == 429 {
				delay = time.Duration(max(rate.retry, 1)) * time.Second
			}
			if !wait(ctx, delay) {
				return
			}
		}
	}
}
func (c *Client) process(ctx context.Context, u app.TelegramInbox) error {
	var update models.Update
	if err := json.Unmarshal(u.Payload, &update); err != nil {
		return c.app.SettleTelegramInput(ctx, u, "unsupported_update")
	}
	if update.CallbackQuery != nil {
		return c.processCallback(ctx, u, update.CallbackQuery)
	}
	m := update.Message
	edited := false
	if m == nil {
		m = update.EditedMessage
		edited = true
	}
	binding, err := c.app.TelegramBinding(ctx)
	if err != nil {
		return err
	}
	if m == nil || m.From == nil || m.Chat.Type != "private" || m.From.ID != binding.UserID || binding.Owner == "" || binding.Status == "disabled" || binding.BotID != c.botID {
		return c.app.SettleTelegramInput(ctx, u, "unbound_sender_or_unsupported_chat")
	}
	done, err := c.app.BeginOwnerMutation(ctx)
	if err != nil {
		return err
	}
	defer done()
	incoming := app.TelegramIncoming{UserID: m.From.ID, ChatID: m.Chat.ID, ExternalID: int64(m.ID), Edited: edited, Forwarded: m.ForwardOrigin != nil, Start: !edited && m.Text == "/start"}
	if m.ReplyToMessage != nil {
		incoming.ReplyExternalID = int64(m.ReplyToMessage.ID)
	}
	incoming.Input.Body = m.Text
	if incoming.Input.Body == "" {
		incoming.Input.Body = m.Caption
	}
	if !incoming.Start {
		if m.Text == "" && m.Caption != "" && m.Document == nil && len(m.Photo) == 0 {
			return c.reject(ctx, u, "unsupported_message_content")
		}
		var fileID, name, media string
		var size int64
		if m.Document != nil {
			fileID = m.Document.FileID
			name = m.Document.FileName
			media = m.Document.MimeType
			size = m.Document.FileSize
		}
		if len(m.Photo) > 0 {
			photo := m.Photo[len(m.Photo)-1]
			fileID = photo.FileID
			name = "photo.jpg"
			media = "image/jpeg"
			size = int64(photo.FileSize)
		}
		if fileID != "" {
			if size > downloadLimit {
				return c.reject(ctx, u, "file_exceeds_download_limit")
			}
			id, err := c.prepareFile(ctx, u, binding, fileID, name, media)
			if err != nil {
				var permanent callError
				if errors.As(err, &permanent) && permanent.code >= 400 && permanent.code < 500 && permanent.code != 408 && permanent.code != 429 {
					return c.reject(ctx, u, "file_unavailable")
				}
				return err
			}
			incoming.Input.Attachments = []string{id}
		}
		if incoming.Input.Body == "" && len(incoming.Input.Attachments) == 0 {
			return c.reject(ctx, u, "unsupported_message_content")
		}
	}
	op, err := c.app.ReceiveTelegram(ctx, u, incoming)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.CodeForbidden {
			return c.app.SettleTelegramInput(ctx, u, "binding_changed")
		}
		return err
	}
	if op.Error != nil {
		return c.reject(ctx, u, "input_rejected_"+string(op.Error.Code))
	}
	return nil
}

func (c *Client) reject(ctx context.Context, u app.TelegramInbox, reason string) error {
	return c.app.RejectTelegramInput(ctx, u, reason)
}

func (c *Client) prepareFile(ctx context.Context, u app.TelegramInbox, b app.TelegramBinding, fileID, name, media string) (string, error) {
	request, err := c.app.TelegramFileRequest(ctx, u, 0)
	if err != nil {
		return "", err
	}
	if prior, ok, err := c.app.TelegramUploadedFile(ctx, request); err != nil || ok {
		return prior, err
	}
	f, err := c.sdk.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", sanitize(err)
	}
	if f.FileSize > downloadLimit {
		return "", callError{code: 400}
	}
	if name == "" {
		name = path.Base(f.FilePath)
	}
	// Never follow a credential-bearing URL to another origin.
	filePath := strings.TrimPrefix(path.Clean("/"+f.FilePath), "/")
	fileURL := c.base + "/file/bot" + c.token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return "", errors.New("telegram file request invalid")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return "", errors.New("telegram file download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", callError{code: response.StatusCode}
	}
	op, err := c.app.UploadConversationAttachment(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: b.Owner, Client: "telegram"}, request, path.Base(strings.ReplaceAll(name, "\\", "/")), media, &boundedReader{r: response.Body, remaining: downloadLimit})
	if err != nil {
		if protocol.CodeOf(err) == protocol.CodeInvalidRequest {
			return "", callError{code: 400}
		}
		return "", err
	}
	if op.Error != nil {
		return "", callError{code: 400}
	}
	var attachment protocol.ConversationAttachment
	if err = json.Unmarshal(op.Result, &attachment); err != nil {
		return "", err
	}
	return attachment.FileID, nil
}

type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var b [1]byte
		n, err := r.r.Read(b[:])
		if n > 0 {
			return 0, callError{code: 400}
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}
func sanitize(err error) error {
	var throttle *bot.TooManyRequestsError
	if errors.As(err, &throttle) {
		return callError{code: 429, retry: throttle.RetryAfter}
	}
	for code, target := range map[int]error{400: bot.ErrorBadRequest, 401: bot.ErrorUnauthorized, 403: bot.ErrorForbidden, 404: bot.ErrorNotFound, 409: bot.ErrorConflict} {
		if errors.Is(err, target) {
			return callError{code: code}
		}
	}
	return errors.New("telegram transport outcome unknown")
}
func resultError(err error) app.DeliveryResult {
	var api callError
	if errors.As(err, &api) {
		if api.code == 429 {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: time.Duration(max(api.retry, 1)) * time.Second, Reason: "telegram_rate_limited"}
		}
		if api.code >= 400 && api.code < 500 {
			return app.DeliveryResult{Outcome: app.Failed, Reason: "telegram_rejected"}
		}
	}
	return app.DeliveryResult{Outcome: app.Unknown, Reason: "telegram_send_outcome_unknown"}
}
func (c *Client) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p app.MessagingPart
	if err := json.Unmarshal(d.Payload, &p); err != nil || p.DeliveryID != d.OutboxID {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid_frozen_delivery"}
	}
	if err := c.app.ValidateMessagingPart(ctx, p); err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "channel_binding_changed"}
	}
	if p.BotID != c.botID {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "bot_identity_changed"}
	}
	if p.Kind == "interaction" {
		return c.editInteraction(ctx, p)
	}
	if p.Kind == "text" {
		params := &bot.SendMessageParams{ChatID: p.ChatID, Text: p.Body}
		if p.ForceReply {
			params.ReplyMarkup = &models.ForceReply{ForceReply: true}
		} else if p.Part == 0 {
			message, err := c.app.MessagingMessage(ctx, p.MessageID)
			if err != nil {
				return app.DeliveryResult{Outcome: app.Retry, Reason: "interaction_projection_unavailable"}
			}
			if i := message.Interaction; i != nil {
				params.Text = boundedInteractionMessage(message)
				params.ReplyMarkup = interactionKeyboard(*i)
			}
		}
		if p.ReplyExternalID != 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: int(p.ReplyExternalID)}
		}
		sent, err := c.sdk.SendMessage(ctx, params)
		if err != nil {
			return resultError(sanitize(err))
		}
		if sent == nil || sent.ID <= 0 {
			return app.DeliveryResult{Outcome: app.Unknown, Reason: "telegram_send_receipt_invalid"}
		}
		return app.DeliveryResult{Outcome: app.Delivered, ExternalMessageID: int64(sent.ID)}
	}
	if p.Kind != "file" {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "unsupported_delivery_kind"}
	}
	return c.sendFile(ctx, p)
}
func (c *Client) Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p app.MessagingPart
	if json.Unmarshal(d.Payload, &p) == nil && p.Kind == "interaction" {
		return c.Deliver(ctx, d)
	}
	return app.DeliveryResult{Outcome: app.Unknown, Reason: "telegram_send_receipt_missing"}
}
func (c *Client) sendFile(ctx context.Context, p app.MessagingPart) app.DeliveryResult {
	meta, file, err := c.app.OpenConversationAttachment(ctx, p.FileID)
	if err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "attachment_unavailable"}
	}
	defer file.Close()
	if meta.Size > uploadLimit {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "attachment_exceeds_upload_limit"}
	}
	method, field := "sendDocument", "document"
	if (meta.MediaType == "image/png" || meta.MediaType == "image/jpeg") && meta.Size <= 10_000_000 {
		method, field = "sendPhoto", "photo"
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := form.WriteField("chat_id", strconv.FormatInt(p.ChatID, 10))
		if err == nil && p.ReplyExternalID != 0 {
			raw, _ := json.Marshal(map[string]int64{"message_id": p.ReplyExternalID})
			err = form.WriteField("reply_parameters", string(raw))
		}
		if err == nil {
			var part io.Writer
			part, err = form.CreateFormFile(field, meta.Name)
			if err == nil {
				_, err = io.Copy(part, file)
			}
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	result, sendErr := c.request(ctx, method, form.FormDataContentType(), reader)
	_ = reader.Close()
	<-done
	if sendErr != nil {
		return resultError(sendErr)
	}
	var message models.Message
	if err = json.Unmarshal(result.Result, &message); err != nil || message.ID <= 0 {
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "telegram_send_receipt_invalid"}
	}
	return app.DeliveryResult{Outcome: app.Delivered, ExternalMessageID: int64(message.ID)}
}
