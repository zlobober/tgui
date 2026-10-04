package tgui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testTransport(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := NewClient("123:SECRET", WithBaseURL(s.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}
func payload(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	defer r.Body.Close()
	if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
		t.Error("unexpected request format")
	}
	var p map[string]any
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["parse_mode"]; ok {
		t.Error("must use explicit entities, never parse_mode")
	}
	return p
}
func reply(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}
func msgResult(id int) map[string]any {
	return map[string]any{"message_id": id, "chat": map[string]any{"id": 42}}
}
func apiError(t *testing.T, err error) *APIError {
	t.Helper()
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("expected APIError, got %v", err)
	}
	rendered := fmt.Sprintf("%v %+v %#v", err, e, e)
	for _, secret := range []string{"SECRET", "sensitive", "https://", "127.0.0.1"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("error leaks secret: %s", rendered)
		}
	}
	return e
}
func TestClientConfiguration(t *testing.T) {
	for _, base := range []string{"http://example.org", "ftp://localhost", "https://u:p@example.org", "https://example.org?token=SECRET", "https://example.org#frag", "https://example.org?", "/relative", "https://"} {
		if _, err := NewClient("token", WithBaseURL(base)); err == nil {
			t.Errorf("accepted %q", base)
		}
	}
	for _, token := range []string{"", "x/y", "x?z", "x\r\n"} {
		if _, err := NewClient(token); err == nil {
			t.Errorf("accepted token %q", token)
		}
	}
	for _, base := range []string{"https://api.telegram.org", "http://localhost:1234", "http://127.0.0.1", "http://[::1]:1234"} {
		if _, err := NewClient("123:abc", WithBaseURL(base)); err != nil {
			t.Fatal(err)
		}
	}
	hc := &http.Client{}
	c, err := NewClient("x", WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	if hc.CheckRedirect != nil || hc.Timeout != 0 || c.http == hc || c.http.Timeout == 0 {
		t.Fatal("HTTP client not safely copied")
	}
}
func TestSendChunksKeyboardAndRefs(t *testing.T) {
	var requests []map[string]any
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, payload(t, r))
		reply(w, msgResult(len(requests)))
	})
	content := strings.Repeat("😀", 3501)
	refs, err := c.Send(context.Background(), 42, Message{Text: Bold(content), Keyboard: [][]Button{{{Label: "OK", Data: "ok"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || len(requests) != 3 {
		t.Fatalf("refs=%v requests=%d", refs, len(requests))
	}
	var joined strings.Builder
	for i, p := range requests {
		text := p["text"].(string)
		joined.WriteString(text)
		if UTF16Len(text) > 3500 {
			t.Error("chunk too big")
		}
		entities := p["entities"].([]any)
		e := entities[0].(map[string]any)
		if e["offset"] != float64(0) || e["length"] != float64(UTF16Len(text)) {
			t.Errorf("bad clipped entity: %v", e)
		}
		_, keyboard := p["reply_markup"]
		if keyboard != (i == 2) {
			t.Error("keyboard must be on final chunk only")
		}
		if refs[i] != (MessageRef{ChatID: 42, MessageID: int64(i + 1)}) {
			t.Error("wrong ref")
		}
	}
	if joined.String() != content {
		t.Fatal("text changed")
	}
}
func TestSendPartialFailureNoRetry(t *testing.T) {
	var calls int
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			reply(w, msgResult(9))
			return
		}
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"sensitive SECRET","parameters":{"retry_after":7}}`)
	})
	refs, err := c.Send(context.Background(), 42, Message{Text: Plain(strings.Repeat("x", 8000))})
	e := apiError(t, err)
	if calls != 2 || !reflect.DeepEqual(refs, []MessageRef{{42, 9}}) || e.Code != 429 || e.RetryAfter != 7*time.Second || e.DeliveryUncertain || e.Unsupported {
		t.Fatalf("calls=%d refs=%v error=%+v", calls, refs, e)
	}
}
func TestEntitySerialization(t *testing.T) {
	var got map[string]any
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) { got = payload(t, r); reply(w, msgResult(1)) })
	text := Text{Content: "abc", Entities: []Entity{{Type: "text_mention", Offset: 0, Length: 1, UserID: 123}, {Type: "date_time", Offset: 1, Length: 1, UnixTime: 1700000000, DateTimeFormat: "wDT"}, {Type: "custom_emoji", Offset: 2, Length: 1, CustomEmojiID: "12345"}}}
	if _, err := c.Send(context.Background(), 42, Message{Text: text}); err != nil {
		t.Fatal(err)
	}
	entities := got["entities"].([]any)
	first := entities[0].(map[string]any)
	if first["user"].(map[string]any)["id"] != float64(123) || first["user_id"] != nil {
		t.Fatalf("invalid mention: %v", first)
	}
	date := entities[1].(map[string]any)
	if date["date_time_format"] != "wDT" || date["unix_time"] != float64(1700000000) || date["format"] != nil {
		t.Fatalf("invalid date-time: %v", date)
	}
}
func TestMarkdownDateTimeWireFormat(t *testing.T) {
	text, err := Markdown("![time](tg://time?unix=1700000000&format=wDT)")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		p := payload(t, r)
		e := p["entities"].([]any)[0].(map[string]any)
		if e["date_time_format"] != "wDT" {
			t.Errorf("bad wire format: %v", e)
		}
		reply(w, msgResult(1))
	})
	if _, err := c.Send(context.Background(), 42, Message{Text: text}); err != nil {
		t.Fatal(err)
	}
}

func TestMethodPayloads(t *testing.T) {
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		p := payload(t, r)
		switch strings.TrimPrefix(r.URL.Path, "/bot123:SECRET/") {
		case "getUpdates":
			if p["offset"] != float64(123) || p["timeout"] != float64(12) {
				t.Errorf("bad polling: %v", p)
			}
			reply(w, []Update{{UpdateID: 123, Message: &IncomingMessage{MessageID: 1, Chat: Chat{ID: 42}, Text: "hi"}}})
		case "setMyCommands":
			if len(p["commands"].([]any)) != 1 {
				t.Error("commands missing")
			}
			reply(w, true)
		case "getMe":
			reply(w, User{ID: 1, IsBot: true, Username: "bot"})
		case "editMessageText":
			if p["message_id"] != float64(9) || p["text"] != "new" || p["reply_markup"] == nil {
				t.Error("bad edit")
			}
			reply(w, msgResult(9))
		case "sendMessageDraft":
			if p["draft_id"] != float64(3) || p["text"] != "" {
				t.Error("bad draft")
			}
			reply(w, true)
		case "answerCallbackQuery":
			if p["callback_query_id"] != "abc" || p["text"] != "done" {
				t.Error("bad callback")
			}
			reply(w, true)
		case "sendChatAction":
			if p["action"] != "typing" {
				t.Error("bad action")
			}
			reply(w, true)
		default:
			t.Error("unexpected method")
		}
	})
	ctx := context.Background()
	updates, err := c.GetUpdates(ctx, 123, 12*time.Second)
	if err != nil || len(updates) != 1 || updates[0].Message.Text != "hi" {
		t.Fatalf("updates: %v %v", updates, err)
	}
	if err = c.SetCommands(ctx, []Command{{"start", "Start bot"}}); err != nil {
		t.Fatal(err)
	}
	me, err := c.Me(ctx)
	if err != nil || me.Username != "bot" {
		t.Fatalf("me: %v %v", me, err)
	}
	for _, err := range []error{c.Edit(ctx, MessageRef{42, 9}, Message{Text: Plain("new")}), c.Draft(ctx, 42, 3, Plain("")), c.AnswerCallback(ctx, "abc", "done"), c.Typing(ctx, 42)} {
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestDocumentMultipart(t *testing.T) {
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("chat_id") != "42" {
			t.Error("wrong chat")
		}
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "hello" || h.Filename != "report.txt" || h.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("invalid document: %s %+v", b, h)
		}
		reply(w, msgResult(10))
	})
	ref, err := c.SendDocument(context.Background(), 42, Document{Filename: "report.txt", MIMEType: "text/plain", Data: []byte("hello")})
	if err != nil || ref != (MessageRef{42, 10}) {
		t.Fatalf("%v %v", ref, err)
	}
}
func TestDraftUnsupportedClassification(t *testing.T) {
	cases := []struct {
		status, code int
		desc         string
		unsupported  bool
	}{
		{404, 404, "Not Found", true},
		{400, 400, "Bad Request: method not found", true},
		{400, 400, "Bad Request: sendMessageDraft is not available", true},
		{400, 400, "Bad Request: drafts are not supported in this chat", true},
		{400, 400, "Bad Request: chat not found", false},
		{400, 400, "Bad Request: invalid draft_id", false},
		{400, 400, "Bad Request: can't parse entities", false},
		{429, 429, "Bad Request: method not found", false},
		{429, 400, "Bad Request: method not found", false},
		{400, 400, "Not Found", false},
		{500, 500, "Bad Request: method not found", false},
		{500, 400, "Bad Request: method not found", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.desc), func(t *testing.T) {
			c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": tc.desc})
			})
			e := apiError(t, c.Draft(context.Background(), 42, 1, Plain("draft")))
			if e.Unsupported != tc.unsupported {
				t.Fatalf("classification %+v", e)
			}
			_, err := c.Send(context.Background(), 42, Message{Text: Plain("message")})
			if apiError(t, err).Unsupported {
				t.Fatal("send error marked draft unsupported")
			}
		})
	}
}
func TestNotModified(t *testing.T) {
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: sensitive SECRET"}`)
	})
	e := apiError(t, c.Edit(context.Background(), MessageRef{42, 1}, Message{Text: Plain("text")}))
	if !e.NotModified || e.DeliveryUncertain || e.Unsupported {
		t.Fatalf("wrong flags: %+v", e)
	}
}
func TestAmbiguousResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server", 500, `{"ok":false,"error_code":500,"description":"sensitive SECRET"}`},
		{"bad json", 200, `sensitive SECRET`},
		{"missing ok", 200, `{}`},
		{"null ok", 200, `{"ok":null}`},
		{"wrong result", 200, `{"ok":true,"result":true}`},
		{"missing ref", 200, `{"ok":true,"result":{}}`},
		{"null result", 200, `{"ok":true,"result":null}`},
		{"oversized", 200, strings.Repeat("x", maxResponseBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.Send(context.Background(), 42, Message{Text: Plain("sensitive")})
			e := apiError(t, err)
			if !e.DeliveryUncertain || e.Unsupported || calls != 1 {
				t.Fatalf("unexpected result: %+v calls=%d", e, calls)
			}
		})
	}
}
func TestRedirectBlocked(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); reply(w, msgResult(1)) }))
	defer target.Close()
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/SECRET", 307) })
	_, err := c.Send(context.Background(), 42, Message{Text: Plain("text")})
	if err == nil || leaked.Load() {
		t.Fatal("followed credential-bearing redirect")
	}
	apiError(t, err)
}
func TestCancellationBeforeAndAfterDispatch(t *testing.T) {
	received := make(chan struct{})
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(received)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Send(ctx, 42, Message{Text: Plain("text")})
	if !errors.Is(err, context.Canceled) || IsUncertain(err) {
		t.Fatalf("preflight: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Send(ctx, 42, Message{Text: Plain("text")}); done <- err }()
	<-received
	cancel()
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation ignored")
	}
	e := apiError(t, err)
	if !e.DeliveryUncertain || !errors.Is(err, context.Canceled) {
		t.Fatalf("inflight: %v", err)
	}
}

func TestTimeoutAndTruncatedResponse(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := c.Draft(ctx, 42, 1, Plain("draft"))
		e := apiError(t, err)
		if !errors.Is(err, context.DeadlineExceeded) || !e.DeliveryUncertain || e.Unsupported {
			t.Fatalf("timeout: %+v", e)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			io.WriteString(w, `{"ok":true,"result":`)
		})
		_, err := c.Send(context.Background(), 42, Message{Text: Plain("text")})
		if !apiError(t, err).DeliveryUncertain {
			t.Fatal("truncated write response must be uncertain")
		}
	})
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("https://SECRET sensitive")
}
func TestTransportErrorRedaction(t *testing.T) {
	c, err := NewClient("SECRET", WithHTTPClient(&http.Client{Transport: failingTransport{}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(context.Background(), 42, Message{Text: Plain("sensitive")})
	if !apiError(t, err).DeliveryUncertain {
		t.Fatal("send transport error not uncertain")
	}
	_, err = c.Me(context.Background())
	if apiError(t, err).DeliveryUncertain {
		t.Fatal("read cannot deliver message")
	}
}
func TestInvalidInputsNeverSend(t *testing.T) {
	var calls atomic.Int64
	c, _ := testTransport(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); reply(w, true) })
	ctx := context.Background()
	checks := []func() error{
		func() error { _, e := c.Send(ctx, 42, Message{Text: Plain("")}); return e },
		func() error { _, e := c.Send(ctx, 0, Message{Text: Plain("hello")}); return e },
		func() error {
			_, e := c.Send(ctx, 42, Message{Text: Plain(strings.Repeat("a", 5000)), Keyboard: [][]Button{{{Label: "x", Data: strings.Repeat("😀", 17)}}}})
			return e
		},
		func() error { return c.Edit(ctx, MessageRef{42, 1}, Message{Text: Plain(strings.Repeat("a", 4097))}) },
		func() error { return c.Draft(ctx, 42, 0, Plain("text")) },
		func() error {
			return c.Draft(ctx, 42, 1, Text{Content: "😀", Entities: []Entity{{Type: "bold", Length: 1}}})
		},
		func() error { _, e := c.GetUpdates(ctx, 0, -time.Second); return e },
		func() error { _, e := c.GetUpdates(ctx, 0, 61*time.Second); return e },
		func() error { return c.SetCommands(ctx, []Command{{"INVALID", "description"}}) },
		func() error { return c.SetCommands(ctx, []Command{{"valid", ""}}) },
		func() error { return c.AnswerCallback(ctx, "id", strings.Repeat("a", 201)) },
		func() error { _, e := c.SendDocument(ctx, 42, Document{Filename: "../x"}); return e },
		func() error {
			_, e := c.SendDocument(ctx, 42, Document{Filename: "a", MIMEType: "text/plain\r\nX: injected"})
			return e
		},
		func() error {
			_, e := c.SendDocument(ctx, 42, Document{Filename: "a", Data: make([]byte, maxDocumentBytes+1)})
			return e
		},
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Errorf("invalid input %d accepted", i)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d invalid requests sent", calls.Load())
	}
}
