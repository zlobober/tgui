package tgui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type responseCall struct {
	kind    string
	text    Text
	message Message
	ref     MessageRef
	draftID int64
}
type responseFake struct {
	calls  []responseCall
	nextID int64
	hook   func(responseCall) ([]MessageRef, error)
}

func (f *responseFake) Draft(_ context.Context, chat ChatID, id int64, text Text) error {
	c := responseCall{kind: "draft", text: text, draftID: id}
	f.calls = append(f.calls, c)
	if f.hook != nil {
		_, err := f.hook(c)
		return err
	}
	return nil
}
func (f *responseFake) Edit(_ context.Context, ref MessageRef, msg Message) error {
	c := responseCall{kind: "edit", text: msg.Text, message: msg, ref: ref}
	f.calls = append(f.calls, c)
	if f.hook != nil {
		_, err := f.hook(c)
		return err
	}
	return nil
}
func (f *responseFake) Send(_ context.Context, chat ChatID, msg Message) ([]MessageRef, error) {
	c := responseCall{kind: "send", text: msg.Text, message: msg}
	f.calls = append(f.calls, c)
	if f.hook != nil {
		refs, err := f.hook(c)
		if refs != nil || err != nil {
			return refs, err
		}
	}
	chunks, err := Split(msg.Text, sendTextUnits)
	if err != nil {
		return nil, err
	}
	var refs []MessageRef
	for range chunks {
		f.nextID++
		refs = append(refs, MessageRef{ChatID: chat, MessageID: f.nextID})
	}
	return refs, nil
}
func responseWithClock(f *responseFake, opts ResponseOptions) (*Response, *time.Time) {
	now := time.Unix(100, 0)
	r := NewResponse(f, 42, opts)
	r.now = func() time.Time { return now }
	return r, &now
}
func TestResponseCoalescesAndThrottles(t *testing.T) {
	f := &responseFake{}
	r, now := responseWithClock(f, ResponseOptions{MinInterval: time.Second})
	r.Update(Plain("old"))
	r.Update(Bold("latest"))
	if len(f.calls) != 0 {
		t.Fatal("Update did network I/O")
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].text.Content != "latest" || len(f.calls[0].text.Entities) != 0 {
		t.Fatal(f.calls)
	}
	*now = now.Add(time.Second)
	_ = r.Flush(context.Background())
	if len(f.calls) != 1 {
		t.Fatal("unchanged preview sent")
	}
	r.Update(Plain("next"))
	_ = r.Flush(context.Background())
	r.Update(Plain("newer"))
	_ = r.Flush(context.Background())
	if len(f.calls) != 2 {
		t.Fatal(f.calls)
	}
	*now = now.Add(time.Second)
	_ = r.Flush(context.Background())
	if len(f.calls) != 3 || f.calls[2].text.Content != "newer" {
		t.Fatal(f.calls)
	}
}
func TestResponsePreviewTail(t *testing.T) {
	f := &responseFake{}
	r, _ := responseWithClock(f, ResponseOptions{MaxPreviewUnits: 5})
	r.Update(Bold("prefix😀end"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.calls[0].text
	if got.Content != "…end" || UTF16Len(got.Content) > 5 || len(got.Entities) != 0 {
		t.Fatal(got)
	}
}
func TestResponseFallbackOnlyDefiniteUnsupported(t *testing.T) {
	tests := []struct {
		name     string
		mode     PreviewMode
		api      *APIError
		fallback bool
	}{
		{"unsupported", PreviewAuto, &APIError{Unsupported: true, Code: 400}, true},
		{"explicit draft", PreviewDraft, &APIError{Unsupported: true, Code: 400}, false},
		{"rate limited", PreviewAuto, &APIError{Code: 429, Unsupported: true, RetryAfter: 10 * time.Second}, false},
		{"timeout", PreviewAuto, &APIError{Unsupported: true, DeliveryUncertain: true}, false},
		{"forbidden", PreviewAuto, &APIError{Code: 403}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &responseFake{hook: func(c responseCall) ([]MessageRef, error) {
				if c.kind == "draft" {
					return nil, tt.api
				}
				return nil, nil
			}}
			r, now := responseWithClock(f, ResponseOptions{Mode: tt.mode})
			r.Update(Plain("preview"))
			err := r.Flush(context.Background())
			if tt.fallback {
				if err != nil || len(f.calls) != 2 || f.calls[1].kind != "send" {
					t.Fatal(err, f.calls)
				}
			} else if err == nil || len(f.calls) != 1 {
				t.Fatal(err, f.calls)
			}
			if tt.api.Code == 429 {
				*now = now.Add(2 * time.Second)
				_ = r.Flush(context.Background())
				if len(f.calls) != 1 {
					t.Fatal("ignored retry-after")
				}
				*now = now.Add(8 * time.Second)
				_ = r.Flush(context.Background())
				if len(f.calls) != 2 {
					t.Fatal("never retried")
				}
			}
		})
	}
}
func TestResponseFinishFullOutputAndIdempotence(t *testing.T) {
	for _, mode := range []PreviewMode{PreviewAuto, PreviewEdit, PreviewNone} {
		t.Run(string(rune('a'+mode)), func(t *testing.T) {
			f := &responseFake{}
			r, _ := responseWithClock(f, ResponseOptions{Mode: mode, MaxPreviewUnits: 10})
			r.Update(Plain("preview"))
			if err := r.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			start := len(f.calls)
			content := strings.Repeat("😀abc", 2000)
			keyboard := [][]Button{{{Label: "done", Data: "done"}}}
			msg := Message{Text: Bold(content), Keyboard: keyboard}
			refs, err := r.Finish(context.Background(), msg)
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) < 2 {
				t.Fatal("output was truncated", refs)
			}
			var text strings.Builder
			var writes []responseCall
			for _, c := range f.calls[start:] {
				if c.kind == "draft" {
					t.Fatal("finalization must not create another draft", c)
				}
				text.WriteString(c.text.Content)
				writes = append(writes, c)
			}
			if text.String() != content {
				t.Fatal("final text lost")
			}
			for i, c := range writes {
				if i == len(writes)-1 {
					if !reflect.DeepEqual(c.message.Keyboard, keyboard) {
						t.Fatal("missing final keyboard")
					}
				} else if len(c.message.Keyboard) != 0 {
					t.Fatal("keyboard on early chunk")
				}
				if len(c.text.Entities) == 0 {
					t.Fatal("lost final entities")
				}
			}
			count := len(f.calls)
			refs[0].MessageID = -100
			again, err := r.Finish(context.Background(), Message{Text: Plain("different")})
			if err != nil || again[0].MessageID == -100 || len(f.calls) != count {
				t.Fatal("not cached", again, err)
			}
			r.Update(Plain("late"))
			_ = r.Flush(context.Background())
			if len(f.calls) != count {
				t.Fatal("preview after final")
			}
		})
	}
}
func TestResponseFinalFailures(t *testing.T) {
	tests := []struct {
		name     string
		refs     []MessageRef
		err      error
		terminal bool
	}{
		{"definite", nil, &APIError{Code: 400}, false},
		{"timeout", nil, &APIError{DeliveryUncertain: true}, true},
		{"unknown", nil, errors.New("connection reset"), true},
		{"partial", []MessageRef{{ChatID: 42, MessageID: 9}}, &APIError{Code: 429}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &responseFake{hook: func(responseCall) ([]MessageRef, error) { return tt.refs, tt.err }}
			r := NewResponse(f, 42, ResponseOptions{Mode: PreviewNone})
			_, err := r.Finish(context.Background(), Message{Text: Plain("final")})
			if errors.Is(err, ErrResponseUncertain) != tt.terminal {
				t.Fatal(err)
			}
			f.hook = nil
			_, err = r.Finish(context.Background(), Message{Text: Plain("final")})
			if tt.terminal {
				if len(f.calls) != 1 || !errors.Is(err, ErrResponseUncertain) {
					t.Fatal("resent uncertain output", err)
				}
			} else if err != nil || len(f.calls) != 2 {
				t.Fatal("rejection not retryable", err)
			}
		})
	}
}
func TestResponsePartialAfterPlaceholderEditIsTerminal(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, 42, ResponseOptions{Mode: PreviewEdit})
	r.Update(Plain("preview"))
	_ = r.Flush(context.Background())
	f.hook = func(c responseCall) ([]MessageRef, error) {
		if c.kind == "send" {
			return nil, &APIError{Code: 429}
		}
		return nil, nil
	}
	refs, err := r.Finish(context.Background(), Message{Text: Plain(strings.Repeat("x", sendTextUnits+1))})
	if len(refs) != 1 || !errors.Is(err, ErrResponseUncertain) {
		t.Fatal(refs, err)
	}
	count := len(f.calls)
	_, _ = r.Finish(context.Background(), Message{Text: Plain("retry")})
	if len(f.calls) != count {
		t.Fatal("resent partial final")
	}
}
func TestResponseAmbiguousPlaceholderNeverDuplicated(t *testing.T) {
	f := &responseFake{hook: func(responseCall) ([]MessageRef, error) { return nil, &APIError{DeliveryUncertain: true} }}
	r, now := responseWithClock(f, ResponseOptions{Mode: PreviewEdit})
	r.Update(Plain("preview"))
	if err := r.Flush(context.Background()); !errors.Is(err, ErrResponseUncertain) {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	_ = r.Flush(context.Background())
	_, _ = r.Finish(context.Background(), Message{Text: Plain("final")})
	if len(f.calls) != 1 {
		t.Fatal("duplicated ambiguous send")
	}
}
func TestResponseConcurrentFlushFinish(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := &responseFake{hook: func(c responseCall) ([]MessageRef, error) {
		if c.kind == "send" && c.text.Content == "preview" {
			close(entered)
			<-release
		}
		return nil, nil
	}}
	r := NewResponse(f, 42, ResponseOptions{Mode: PreviewEdit})
	r.Update(Plain("preview"))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := r.Flush(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	go func() {
		defer wg.Done()
		if _, err := r.Finish(context.Background(), Message{Text: Plain("final")}); err != nil {
			t.Error(err)
		}
	}()
	close(release)
	wg.Wait()
	if len(f.calls) != 2 || f.calls[0].kind != "send" || f.calls[1].kind != "edit" || f.calls[1].text.Content != "final" {
		t.Fatal(f.calls)
	}
}
func TestResponseCancelledFinishCanUseFreshContext(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, 42, ResponseOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Finish(ctx, Message{Text: Plain("notice")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("used cancelled context")
	}
	if _, err := r.Finish(context.Background(), Message{Text: Plain("notice")}); err != nil {
		t.Fatal(err)
	}
}
func TestResponseNotModifiedEditIsSuccess(t *testing.T) {
	f := &responseFake{}
	r, now := responseWithClock(f, ResponseOptions{Mode: PreviewEdit})
	r.Update(Plain("preview"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.hook = func(c responseCall) ([]MessageRef, error) {
		if c.kind == "edit" {
			return nil, &APIError{Code: 400, NotModified: true}
		}
		return nil, nil
	}
	*now = now.Add(time.Second)
	r.Update(Plain("changed"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	count := len(f.calls)
	_ = r.Flush(context.Background())
	if len(f.calls) != count {
		t.Fatal("not-modified preview retried")
	}
	refs, err := r.Finish(context.Background(), Message{Text: Plain("changed")})
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	count = len(f.calls)
	_, err = r.Finish(context.Background(), Message{Text: Plain("changed")})
	if err != nil || len(f.calls) != count {
		t.Fatal("not-modified final retried", err)
	}
}

func TestResponseFinishDoesNotRecreateDraft(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, 42, ResponseOptions{})
	r.Update(Plain("preview"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.hook = func(c responseCall) ([]MessageRef, error) {
		if c.kind == "draft" {
			return nil, &APIError{Code: 400, Unsupported: true}
		}
		return nil, nil
	}
	if _, err := r.Finish(context.Background(), Message{Text: Plain("final")}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].kind != "draft" || f.calls[1].kind != "send" {
		t.Fatal("expected preview followed only by final send", f.calls)
	}
	if _, err := r.Finish(context.Background(), Message{Text: Plain("final")}); err != nil {
		t.Fatal(err)
	}
	r.Update(Plain("late progress"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatal("finalized response created another draft or duplicate final")
	}
}

func TestResponseEmptyFinalDoesNotPanic(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, 42, ResponseOptions{Mode: PreviewEdit})
	r.Update(Plain("preview"))
	_ = r.Flush(context.Background())
	if _, err := r.Finish(context.Background(), Message{}); err == nil {
		t.Fatal("empty final accepted")
	}
	if _, err := r.Finish(context.Background(), Message{Text: Plain("fixed")}); err != nil {
		t.Fatal(err)
	}
}
