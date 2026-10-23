package tgui

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// ResponseTransport is the write-only transport used by a Response.
type ResponseTransport interface {
	Draft(context.Context, ChatID, int64, Text) error
	Send(context.Context, ChatID, Message) ([]MessageRef, error)
	Edit(context.Context, MessageRef, Message) error
}

type PreviewMode int

const (
	PreviewAuto PreviewMode = iota
	PreviewDraft
	PreviewEdit
	PreviewNone
)

type ResponseOptions struct {
	Mode PreviewMode
	// ThreadID scopes every send to a forum topic. Topic previews use edits,
	// never the unscoped Draft API, including when Mode is Auto or Draft.
	ThreadID           int64
	DisableLinkPreview bool
	// MinInterval is the minimum interval between preview attempts. Zero uses one second.
	MinInterval time.Duration
	// MaxPreviewUnits bounds previews in UTF-16 units. Zero uses the transport chunk limit.
	MaxPreviewUnits int
}

// ErrResponseUncertain means this response must not be retried: some output may
// already have been delivered. A new Response is not a safe automatic retry.
var ErrResponseUncertain = errors.New("response delivery is uncertain; do not resend")
var responseDraftID atomic.Int64

// Response coalesces explicit Flush calls without timers or background workers.
// Network calls are serialized per response, not per client or chat. The caller
// owns cancellation; a cancellation notice requires Finish with a fresh context.
type Response struct {
	mu          sync.Mutex
	transport   ResponseTransport
	chat        ChatID
	opts        ResponseOptions
	now         func() time.Time
	draftID     int64
	mode        PreviewMode
	desired     Text
	formatted   bool
	markdown    *string
	last        Text
	hasLast     bool
	next        time.Time
	placeholder *MessageRef
	done        bool
	refs        []MessageRef
	terminal    error
}

func NewResponse(transport ResponseTransport, chat ChatID, opts ResponseOptions) *Response {
	if opts.ThreadID != 0 && (opts.Mode == PreviewAuto || opts.Mode == PreviewDraft) {
		opts.Mode = PreviewEdit
	}
	if opts.MinInterval == 0 {
		opts.MinInterval = time.Second
	}
	if opts.MaxPreviewUnits <= 0 || opts.MaxPreviewUnits > sendTextUnits {
		opts.MaxPreviewUnits = sendTextUnits
	}
	return &Response{transport: transport, chat: chat, opts: opts, mode: opts.Mode, now: time.Now, draftID: responseDraftID.Add(1)}
}
func (c *Client) NewResponse(chat ChatID, opts ResponseOptions) *Response {
	return NewResponse(c, chat, opts)
}

// Update replaces the desired preview. It never performs network I/O.
func (r *Response) Update(text Text) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.terminal != nil {
		return
	}
	// Previews deliberately omit formatting; final messages retain their entities.
	r.desired = Plain(text.Content)
	r.formatted = false
	r.markdown = nil
}

// UpdateFormatted replaces a cumulative preview with validated native entities.
// It makes no network calls and copies entities so callers retain ownership.
// Use this for formats other than Telegram MarkdownV2 (e.g. CommonMark renderers).
func (r *Response) UpdateFormatted(text Text) error {
	if err := validateText(text); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.terminal != nil {
		return nil
	}
	r.desired = Text{Content: text.Content, Entities: append([]Entity(nil), text.Entities...)}
	r.formatted = true
	r.markdown = nil
	return nil
}

// UpdateMarkdown replaces cumulative Markdown source, not a delta. Parsing is
// deferred to Flush. Prefix selection never changes the caller's final answer.
func (r *Response) UpdateMarkdown(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.terminal != nil {
		return
	}
	r.markdown = &source
}

func previewTail(text string, limit int) string {
	if UTF16Len(text) <= limit {
		return text
	}
	const marker = "…"
	budget := limit - 1
	runes := []rune(text)
	start := len(runes)
	for start > 0 {
		units := 1
		if runes[start-1] > 0xffff {
			units = 2
		}
		if units > budget {
			break
		}
		budget -= units
		start--
	}
	return marker + string(runes[start:])
}

func (r *Response) rateLimit(err error) {
	var api *APIError
	if errors.As(err, &api) && api.Code == 429 && api.RetryAfter > 0 {
		next := r.now().Add(api.RetryAfter)
		if next.After(r.next) {
			r.next = next
		}
	}
}
func editResult(err error) error {
	var api *APIError
	if errors.As(err, &api) && api.NotModified && !api.DeliveryUncertain {
		return nil
	}
	return err
}

func definiteRejection(err error) bool {
	var api *APIError
	return errors.As(err, &api) && !api.DeliveryUncertain
}
func (r *Response) uncertain(err error, refs []MessageRef) error {
	r.refs = append([]MessageRef(nil), refs...)
	r.terminal = errors.Join(ErrResponseUncertain, err)
	return r.terminal
}

// Flush sends at most the latest bounded preview. Throttled and unchanged
// previews are no-ops. A 429 schedules the next attempt without sleeping.
func (r *Response) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal != nil {
		return r.terminal
	}
	if r.done || r.mode == PreviewNone {
		return nil
	}
	if r.now().Before(r.next) {
		return nil
	}
	var text Text
	if r.markdown != nil {
		r.next = r.now().Add(r.opts.MinInterval) // Bound parsing work as well as network writes.
		var ok bool
		text, ok = markdownPreview(*r.markdown, r.opts.MaxPreviewUnits)
		if !ok {
			r.next = r.now().Add(r.opts.MinInterval)
			return nil // Keep the previous preview instead of exposing raw markup.
		}
	} else if r.formatted {
		text = formattedPreviewTail(r.desired, r.opts.MaxPreviewUnits)
	} else {
		text = Plain(previewTail(r.desired.Content, r.opts.MaxPreviewUnits))
	}
	key := text // Include all entity fields, including non-JSON user IDs.
	if text.Content == "" || (r.hasLast && reflect.DeepEqual(r.last, key)) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.next = r.now().Add(r.opts.MinInterval)
	if r.mode == PreviewAuto || r.mode == PreviewDraft {
		err := r.transport.Draft(ctx, r.chat, r.draftID, text)
		if err == nil {
			r.last = key
			r.hasLast = true
			return nil
		}
		r.rateLimit(err)
		var api *APIError
		if r.mode != PreviewAuto || !errors.As(err, &api) || !api.Unsupported || api.DeliveryUncertain || api.Code == 429 {
			return err
		}
		r.mode = PreviewEdit
	}
	var err error
	if r.placeholder == nil {
		var refs []MessageRef
		refs, err = r.transport.Send(ctx, r.chat, Message{Text: text, ThreadID: r.opts.ThreadID, DisableLinkPreview: r.opts.DisableLinkPreview})
		if err != nil {
			r.rateLimit(err)
			if len(refs) > 0 || !definiteRejection(err) {
				return r.uncertain(err, refs)
			}
			return err
		}
		if len(refs) != 1 {
			return r.uncertain(errors.New("preview transport returned unexpected message count"), refs)
		}
		ref := refs[0]
		r.placeholder = &ref
	} else {
		err = editResult(r.transport.Edit(ctx, *r.placeholder, Message{Text: text, ThreadID: r.opts.ThreadID, DisableLinkPreview: r.opts.DisableLinkPreview}))
	}
	if err != nil {
		r.rateLimit(err)
		return err
	}
	r.last = key
	r.hasLast = true
	return nil
}

// Finish publishes the complete final message. Success is idempotent. Partial
// or ambiguous delivery is terminal; definite rejection before any effects can
// be retried. Send owns splitting for messages without an edit placeholder.
func (r *Response) Finish(ctx context.Context, message Message) ([]MessageRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal != nil {
		return append([]MessageRef(nil), r.refs...), r.terminal
	}
	if r.done {
		return append([]MessageRef(nil), r.refs...), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.opts.ThreadID != 0 {
		message.ThreadID = r.opts.ThreadID
	}
	if r.opts.DisableLinkPreview {
		message.DisableLinkPreview = true
	}
	chunks, err := Split(message.Text, sendTextUnits)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, errors.New("response final text must not be empty")
	}
	// sendMessage retires the ephemeral draft. Do not send another draft after
	// final delivery: whitespace creates a blank bubble, and empty text requests
	// a new Thinking placeholder. On delivery failure the draft expires naturally.
	var refs []MessageRef
	if r.placeholder == nil {
		refs, err = r.transport.Send(ctx, r.chat, message)
	} else {
		first := Message{Text: chunks[0], ThreadID: message.ThreadID, DisableLinkPreview: message.DisableLinkPreview}
		if len(chunks) == 1 {
			first.Keyboard = message.Keyboard
		}
		err = editResult(r.transport.Edit(ctx, *r.placeholder, first))
		if err == nil {
			refs = append(refs, *r.placeholder)
			for i := 1; i < len(chunks); i++ {
				part := Message{Text: chunks[i], ThreadID: message.ThreadID, DisableLinkPreview: message.DisableLinkPreview}
				if i == len(chunks)-1 {
					part.Keyboard = message.Keyboard
				}
				var sent []MessageRef
				sent, err = r.transport.Send(ctx, r.chat, part)
				refs = append(refs, sent...)
				if err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		r.rateLimit(err)
		if len(refs) > 0 || !definiteRejection(err) {
			return append([]MessageRef(nil), refs...), r.uncertain(err, refs)
		}
		return nil, err
	}
	r.done = true
	r.refs = append([]MessageRef(nil), refs...)
	return append([]MessageRef(nil), refs...), nil
}
