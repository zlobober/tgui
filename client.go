package tgui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxResponseBytes = 8 << 20
	maxDocumentBytes = 20 << 20
)

type Client struct {
	endpoint string
	http     *http.Client
}
type clientOptions struct {
	baseURL string
	http    *http.Client
}
type ClientOption func(*clientOptions)

func WithHTTPClient(client *http.Client) ClientOption {
	return func(o *clientOptions) { o.http = client }
}
func WithBaseURL(baseURL string) ClientOption { return func(o *clientOptions) { o.baseURL = baseURL } }

// NewClient copies the HTTP client and disables redirects, preventing token
// disclosure through redirect URLs. HTTP is allowed only for loopback servers.
func NewClient(token string, opts ...ClientOption) (*Client, error) {
	if token == "" || len(token) > 256 {
		return nil, errors.New("telegram: invalid token")
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '-') {
			return nil, errors.New("telegram: invalid token")
		}
	}
	o := clientOptions{baseURL: "https://api.telegram.org"}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	u, err := url.Parse(o.baseURL)
	if err != nil {
		return nil, errors.New("telegram: invalid base URL")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback)) {
		return nil, errors.New("telegram: invalid base URL")
	}
	hc := http.Client{Timeout: 70 * time.Second}
	if o.http != nil {
		hc = *o.http
	}
	if hc.Timeout <= 0 {
		hc.Timeout = 70 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: strings.TrimRight(u.String(), "/") + "/bot" + token + "/", http: &hc}, nil
}

func writeMethod(method string) bool { return method != "getMe" && method != "getUpdates" }

func (c *Client) request(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	apiErr := &APIError{Method: method}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, body)
	if err != nil {
		return apiErr
	}
	// Do not opt POSTs into replay or idempotency-based transport retries.
	req.GetBody = nil
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	uncertain := func() error {
		apiErr.DeliveryUncertain = writeMethod(method)
		if ctx.Err() != nil {
			return errors.Join(apiErr, ctx.Err())
		}
		return apiErr
	}
	if err != nil {
		return uncertain()
	}
	defer resp.Body.Close()
	apiErr.Code = resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return uncertain()
	}
	var envelope struct {
		OK          *bool           `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.OK == nil {
		return uncertain()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !*envelope.OK {
		if envelope.Code >= 400 && envelope.Code <= 599 {
			apiErr.Code = envelope.Code
		}
		if envelope.Parameters.RetryAfter > 0 {
			// Clamp before duration conversion, avoiding attacker-controlled overflow.
			seconds := min(envelope.Parameters.RetryAfter, int64((1<<63-1)/int64(time.Second)))
			apiErr.RetryAfter = time.Duration(seconds) * time.Second
		}
		apiErr.DeliveryUncertain = writeMethod(method) && (resp.StatusCode >= 500 || apiErr.Code >= 500)
		desc := strings.ToLower(strings.TrimSpace(envelope.Description))
		// The raw description never escapes this function. Match narrow canonical
		// errors, not arbitrary 400s (bad chat/text), throttles or transient failures.
		if !*envelope.OK && apiErr.Code == 400 && (resp.StatusCode == 200 || resp.StatusCode == 400) && method == "editMessageText" {
			apiErr.NotModified = desc == "bad request: message is not modified" || strings.HasPrefix(desc, "bad request: message is not modified: ")
		}
		if !*envelope.OK && method == "sendMessageDraft" && (apiErr.Code == 400 || apiErr.Code == 404) && (resp.StatusCode == 200 || resp.StatusCode == 400 || resp.StatusCode == 404) {
			switch desc {
			case "not found":
				apiErr.Unsupported = apiErr.Code == 404
			case "bad request: method not found", "bad request: method is not available", "bad request: sendmessagedraft is not available", "bad request: sendmessagedraft is not supported", "bad request: drafts are not supported in this chat", "bad request: drafts are only supported in private chats", "bad request: method is available only in private chats":
				apiErr.Unsupported = true
			}
		}
		return apiErr
	}
	if result != nil && (len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) || json.Unmarshal(envelope.Result, result) != nil) {
		return uncertain()
	}
	return nil
}
func (c *Client) json(ctx context.Context, method string, payload, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.New("telegram: invalid request")
	}
	return c.request(ctx, method, "application/json", bytes.NewReader(data), result)
}
func (c *Client) boolean(ctx context.Context, method string, payload any) error {
	var ok bool
	if err := c.json(ctx, method, payload, &ok); err != nil {
		return err
	}
	if !ok {
		return &APIError{Method: method, DeliveryUncertain: writeMethod(method)}
	}
	return nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	if wait < 0 || wait > 60*time.Second {
		return nil, errors.New("telegram: invalid polling wait")
	}
	var out []Update
	err := c.json(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int64(wait / time.Second), "allowed_updates": []string{"message", "callback_query"}}, &out)
	return out, err
}
func (c *Client) SetCommands(ctx context.Context, commands []Command) error {
	if len(commands) > 100 {
		return errors.New("telegram: too many commands")
	}
	for _, cmd := range commands {
		if len(cmd.Command) < 1 || len(cmd.Command) > 32 || !validString(cmd.Description, 1, 256) {
			return errors.New("telegram: invalid command")
		}
		for _, r := range cmd.Command {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return errors.New("telegram: invalid command")
			}
		}
	}
	if commands == nil {
		commands = []Command{}
	}
	return c.boolean(ctx, "setMyCommands", map[string]any{"commands": commands})
}
func validString(s string, lo, hi int) bool {
	return len(s) <= hi*4 && utf8.ValidString(s) && UTF16Len(s) >= lo && UTF16Len(s) <= hi
}

// entityPayload follows Bot API MessageEntity, notably user.id (not user_id)
// and date_time_format. See https://core.telegram.org/bots/api#messageentity.
func entityPayload(entities []Entity) []map[string]any {
	out := make([]map[string]any, 0, len(entities))
	for _, e := range entities {
		p := map[string]any{"type": e.Type, "offset": e.Offset, "length": e.Length}
		switch e.Type {
		case "text_link":
			p["url"] = e.URL
		case "text_mention":
			p["user"] = map[string]any{"id": e.UserID}
		case "pre":
			if e.Language != "" {
				p["language"] = e.Language
			}
		case "custom_emoji":
			p["custom_emoji_id"] = e.CustomEmojiID
		case "date_time":
			p["unix_time"] = e.UnixTime
			p["date_time_format"], _ = wireDateTimeFormat(e.DateTimeFormat)
		}
		out = append(out, p)
	}
	return out
}
func keyboardPayload(rows [][]Button) (map[string]any, error) {
	out := make([][]map[string]string, 0, len(rows))
	count := 0
	if len(rows) > 100 {
		return nil, errors.New("telegram: invalid keyboard")
	}
	for _, row := range rows {
		if len(row) == 0 || len(row) > 8 {
			return nil, errors.New("telegram: invalid keyboard")
		}
		buttons := make([]map[string]string, 0, len(row))
		for _, b := range row {
			count++
			if count > 100 || !validString(b.Label, 1, 256) || !utf8.ValidString(b.Data) || len(b.Data) < 1 || len(b.Data) > 64 {
				return nil, errors.New("telegram: invalid button")
			}
			buttons = append(buttons, map[string]string{"text": b.Label, "callback_data": b.Data})
		}
		out = append(out, buttons)
	}
	return map[string]any{"inline_keyboard": out}, nil
}
func textPayload(chat ChatID, t Text) map[string]any {
	return map[string]any{"chat_id": chat, "text": t.Content, "entities": entityPayload(t.Entities)}
}
func (c *Client) sendResult(ctx context.Context, method string, payload any, chat ChatID) (MessageRef, error) {
	var msg IncomingMessage
	if err := c.json(ctx, method, payload, &msg); err != nil {
		return MessageRef{}, err
	}
	if msg.MessageID <= 0 || msg.Chat.ID != int64(chat) {
		return MessageRef{}, &APIError{Method: method, DeliveryUncertain: true}
	}
	return MessageRef{ChatID: chat, MessageID: msg.MessageID}, nil
}

// Send makes no retries. If a later chunk fails, refs contains only confirmed
// earlier sends; an uncertain failed chunk must not be blindly resent.
func (c *Client) Send(ctx context.Context, chat ChatID, msg Message) ([]MessageRef, error) {
	if chat == 0 || msg.Text.Content == "" {
		return nil, errors.New("telegram: invalid message")
	}
	chunks, err := Split(msg.Text, sendTextUnits)
	if err != nil {
		return nil, err
	}
	keyboard, err := keyboardPayload(msg.Keyboard)
	if err != nil {
		return nil, err
	}
	refs := make([]MessageRef, 0, len(chunks))
	for i, chunk := range chunks {
		p := textPayload(chat, chunk)
		if i == len(chunks)-1 && len(msg.Keyboard) > 0 {
			p["reply_markup"] = keyboard
		}
		ref, err := c.sendResult(ctx, "sendMessage", p, chat)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
func (c *Client) Edit(ctx context.Context, ref MessageRef, msg Message) error {
	if ref.ChatID == 0 || ref.MessageID <= 0 || !validString(msg.Text.Content, 1, 4096) {
		return errors.New("telegram: invalid edit")
	}
	if err := validateText(msg.Text); err != nil {
		return err
	}
	keyboard, err := keyboardPayload(msg.Keyboard)
	if err != nil {
		return err
	}
	p := textPayload(ref.ChatID, msg.Text)
	p["message_id"] = ref.MessageID
	p["reply_markup"] = keyboard
	_, err = c.sendResult(ctx, "editMessageText", p, ref.ChatID)
	return err
}
func (c *Client) Draft(ctx context.Context, chat ChatID, draftID int64, text Text) error {
	if chat == 0 || draftID == 0 || !validString(text.Content, 0, 4096) {
		return errors.New("telegram: invalid draft")
	}
	if err := validateText(text); err != nil {
		return err
	}
	p := textPayload(chat, text)
	p["draft_id"] = draftID
	return c.boolean(ctx, "sendMessageDraft", p)
}
func (c *Client) SendDocument(ctx context.Context, chat ChatID, doc Document) (MessageRef, error) {
	if chat == 0 || len(doc.Data) > maxDocumentBytes || len(doc.Filename) < 1 || len(doc.Filename) > 255 || !utf8.ValidString(doc.Filename) || strings.ContainsAny(doc.Filename, "\r\n\x00/\\") {
		return MessageRef{}, errors.New("telegram: invalid document")
	}
	contentType := doc.MIMEType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(contentType) > 255 || strings.ContainsAny(contentType, "\r\n\x00") {
		return MessageRef{}, errors.New("telegram: invalid document MIME type")
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return MessageRef{}, errors.New("telegram: invalid document MIME type")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(int64(chat), 10)); err != nil {
		return MessageRef{}, errors.New("telegram: cannot encode document")
	}
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "document", "filename": doc.Filename}))
	headers.Set("Content-Type", contentType)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return MessageRef{}, errors.New("telegram: cannot encode document")
	}
	if _, err = part.Write(doc.Data); err != nil {
		return MessageRef{}, errors.New("telegram: cannot encode document")
	}
	if err = writer.Close(); err != nil {
		return MessageRef{}, errors.New("telegram: cannot encode document")
	}
	var msg IncomingMessage
	if err = c.request(ctx, "sendDocument", writer.FormDataContentType(), &body, &msg); err != nil {
		return MessageRef{}, err
	}
	if msg.MessageID <= 0 || msg.Chat.ID != int64(chat) {
		return MessageRef{}, &APIError{Method: "sendDocument", DeliveryUncertain: true}
	}
	return MessageRef{ChatID: chat, MessageID: msg.MessageID}, nil
}
func (c *Client) AnswerCallback(ctx context.Context, id, notice string) error {
	if !validString(id, 1, 256) || !validString(notice, 0, 200) {
		return errors.New("telegram: invalid callback answer")
	}
	return c.boolean(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": notice})
}
func (c *Client) Typing(ctx context.Context, chat ChatID) error {
	if chat == 0 {
		return errors.New("telegram: invalid chat")
	}
	return c.boolean(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"})
}
func (c *Client) Me(ctx context.Context) (User, error) {
	var user User
	err := c.json(ctx, "getMe", struct{}{}, &user)
	if err == nil && user.ID <= 0 {
		err = &APIError{Method: "getMe"}
	}
	return user, err
}
