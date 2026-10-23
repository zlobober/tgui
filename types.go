// Package tgui provides agent-independent Telegram transport and presentation.
// It owns neither polling offsets, user authorization, model calls nor sessions.
package tgui

import (
	"errors"
	"time"
	"unicode/utf16"

	"github.com/zlobober/tgui/markdownv2"
)

type ChatID int64
type Entity = markdownv2.Entity
type Text struct {
	Content  string
	Entities []Entity
}
type MessageRef struct {
	ChatID    ChatID
	MessageID int64
}
type Message struct {
	Text     Text
	Keyboard [][]Button
	// ThreadID targets a forum topic. Zero uses the chat's default destination.
	ThreadID           int64
	DisableLinkPreview bool
}
type Button struct {
	Label string
	Data  string
}
type Document struct {
	Filename string
	MIMEType string
	Data     []byte
}
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type IncomingMessage struct {
	MessageID       int64  `json:"message_id"`
	From            *User  `json:"from"`
	Chat            Chat   `json:"chat"`
	Text            string `json:"text"`
	MessageThreadID int64  `json:"message_thread_id"`
	Date            int64  `json:"date"`
	SenderChat      *Chat  `json:"sender_chat"`
}
type CallbackQuery struct {
	ID      string           `json:"id"`
	From    User             `json:"from"`
	Message *IncomingMessage `json:"message"`
	Data    string           `json:"data"`
}
type Update struct {
	UpdateID      int64            `json:"update_id"`
	Message       *IncomingMessage `json:"message"`
	CallbackQuery *CallbackQuery   `json:"callback_query"`
}

// APIError never includes a token-bearing URL, raw response, or user text.
// DeliveryUncertain means a write may have reached Telegram: never blindly resend.
type APIError struct {
	Method            string
	Code              int
	RetryAfter        time.Duration
	DeliveryUncertain bool
	Unsupported       bool
	NotModified       bool
}

func (e *APIError) Error() string { return "Telegram " + e.Method + " request failed" }
func IsUncertain(err error) bool  { var e *APIError; return errors.As(err, &e) && e.DeliveryUncertain }

type Diagnostic struct {
	Message    string
	ByteOffset int
}

func FormattingInstructions() string { return markdownv2.FormattingInstructions() }
func Validate(source string) []Diagnostic {
	if _, err := Markdown(source); err != nil {
		offset := -1
		var p *markdownv2.ParseError
		if errors.As(err, &p) {
			offset = p.ByteOffset
		}
		return []Diagnostic{{Message: err.Error(), ByteOffset: offset}}
	}
	return nil
}
func Markdown(source string) (Text, error) {
	if len(source) > maxTextBytes {
		return Text{}, errors.New("Markdown exceeds the input size limit")
	}
	r, err := markdownv2.Parse(source)
	if err != nil {
		return Text{}, err
	}
	t := Text{Content: r.Text, Entities: r.Entities}
	for i := range t.Entities {
		if t.Entities[i].Type == "date_time" {
			format, e := transportDateFormat(t.Entities[i].DateTimeFormat)
			if e != nil {
				return Text{}, e
			}
			t.Entities[i].DateTimeFormat = format
		}
	}
	if err := validateText(t); err != nil {
		return Text{}, errors.New("Parsed formatting cannot be sent safely: avoid overlapping code/entities or unsupported link/date forms")
	}
	return t, nil
}

// TDLib stores independent flags; the Bot API uses weekday/date/time order.
// Reject conflicting short/long forms rather than silently changing their meaning.
func transportDateFormat(s string) (string, error) {
	if s == "r" {
		return s, nil
	}
	flags := map[rune]bool{}
	for _, r := range s {
		switch r {
		case 'w', 'd', 'D', 't', 'T':
			flags[r] = true
		default:
			return "", errors.New("unsupported date formatting flag")
		}
	}
	if flags['d'] && flags['D'] || flags['t'] && flags['T'] {
		return "", errors.New("choose one short or long format for each date/time component")
	}
	out := ""
	for _, r := range "wdDtT" {
		if flags[r] {
			out += string(r)
		}
	}
	return out, nil
}
func Plain(s string) Text   { return Text{Content: s} }
func UTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }
func styled(s, kind string) Text {
	t := Plain(s)
	if s != "" {
		t.Entities = []Entity{{Type: kind, Offset: 0, Length: UTF16Len(s)}}
	}
	return t
}
func Bold(s string) Text   { return styled(s, "bold") }
func Italic(s string) Text { return styled(s, "italic") }
func Code(s string) Text   { return styled(s, "code") }
func Pre(language, s string) Text {
	t := styled(s, "pre")
	if len(t.Entities) > 0 {
		t.Entities[0].Language = language
	}
	return t
}
func Link(label, url string) Text {
	t := styled(label, "text_link")
	if len(t.Entities) > 0 {
		t.Entities[0].URL = url
	}
	return t
}
func Join(parts ...Text) Text {
	var out Text
	offset := 0
	for _, p := range parts {
		out.Content += p.Content
		for _, e := range p.Entities {
			e.Offset += offset
			out.Entities = append(out.Entities, e)
		}
		offset += UTF16Len(p.Content)
	}
	return out
}
