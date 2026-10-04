package tgui

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxTextBytes  = 1 << 20
	maxEntities   = 1024
	sendTextUnits = 3500
)

var dateTimeFormat = regexp.MustCompile(`^(r|w?[dD]?[tT]?)$`)

// Split preserves every byte of valid UTF-8 text, splitting only at rune
// boundaries. Entity ranges are clipped to each chunk and rebased in UTF-16
// units. A rune wider than the requested limit is an error, not a lossy split.
// Text is bounded to 1 MiB and 1024 entities before allocating chunk data.
func Split(text Text, maxUTF16 int) ([]Text, error) {
	if maxUTF16 <= 0 {
		return nil, errors.New("telegram: invalid chunk limit")
	}
	if err := validateText(text); err != nil {
		return nil, err
	}
	if text.Content == "" {
		return []Text{}, nil
	}
	// Custom emoji must cover a complete emoji (possibly several runes), and
	// splitting a date entity would render the date twice. Keep these atomic.
	atomicStarts := map[int]int{}
	for _, e := range text.Entities {
		if e.Type == "custom_emoji" || e.Type == "date_time" {
			if e.Length > maxUTF16 {
				return nil, errors.New("telegram: chunk limit cannot fit atomic entity")
			}
			atomicStarts[e.Offset] = e.Length
		}
	}
	var chunks []Text
	startByte, startUnit, units := 0, 0, 0
	appendChunk := func(end int) {
		chunk := Text{Content: text.Content[startByte:end]}
		for _, e := range text.Entities {
			lo, hi := max(e.Offset, startUnit), min(e.Offset+e.Length, startUnit+units)
			if lo < hi {
				e.Offset, e.Length = lo-startUnit, hi-lo
				chunk.Entities = append(chunk.Entities, e)
			}
		}
		chunks = append(chunks, chunk)
		startByte, startUnit, units = end, startUnit+units, 0
	}
	for pos, r := range text.Content {
		if length := atomicStarts[startUnit+units]; length > 0 && units+length > maxUTF16 {
			appendChunk(pos)
		}
		width := 1
		if r > 0xffff {
			width = 2
		}
		if width > maxUTF16 {
			return nil, errors.New("telegram: chunk limit cannot fit rune")
		}
		if units+width > maxUTF16 {
			appendChunk(pos)
		}
		units += width
	}
	appendChunk(len(text.Content))
	return chunks, nil
}

func validateText(text Text) error {
	invalid := func() error { return errors.New("telegram: invalid text or entities") }
	if len(text.Content) > maxTextBytes || !utf8.ValidString(text.Content) || len(text.Entities) > maxEntities {
		return invalid()
	}
	boundaries := make([]bool, len(text.Content)+1)
	units := 0
	boundaries[0] = true
	for _, r := range text.Content {
		units++
		if r > 0xffff {
			units++
		}
		boundaries[units] = true
	}
	for _, e := range text.Entities {
		if e.Offset < 0 || e.Length <= 0 || e.Offset > units || e.Length > units-e.Offset || !boundaries[e.Offset] || !boundaries[e.Offset+e.Length] {
			return invalid()
		}
		switch e.Type {
		case "mention", "hashtag", "cashtag", "bot_command", "url", "email", "phone_number", "bold", "italic", "underline", "strikethrough", "spoiler", "blockquote", "expandable_blockquote", "code", "pre", "text_link", "text_mention", "custom_emoji", "date_time":
		default:
			return invalid()
		}
		if e.Type != "text_link" && e.URL != "" || e.Type != "text_mention" && e.UserID != 0 || e.Type != "pre" && e.Language != "" || e.Type != "custom_emoji" && e.CustomEmojiID != "" || e.Type != "date_time" && (e.UnixTime != 0 || e.DateTimeFormat != "") {
			return invalid()
		}
		if len(e.Language) > 128 || !utf8.ValidString(e.Language) || strings.ContainsAny(e.Language, "\r\n\x00") {
			return invalid()
		}
		switch e.Type {
		case "text_link":
			if len(e.URL) > 4096 || !utf8.ValidString(e.URL) || strings.ContainsAny(e.URL, "\r\n\x00") {
				return invalid()
			}
			u, err := url.Parse(e.URL)
			if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "tg" && u.Scheme != "mailto") || (u.Host == "" && u.Scheme != "mailto") {
				return invalid()
			}
		case "text_mention":
			if e.UserID <= 0 {
				return invalid()
			}
		case "custom_emoji":
			id, err := strconv.ParseInt(e.CustomEmojiID, 10, 64)
			if err != nil || id == 0 || strconv.FormatInt(id, 10) != e.CustomEmojiID {
				return invalid()
			}
		case "date_time":
			if _, ok := wireDateTimeFormat(e.DateTimeFormat); e.UnixTime <= 0 || !ok {
				return invalid()
			}
		}
	}
	for i, a := range text.Entities {
		for _, b := range text.Entities[i+1:] {
			ae, be := a.Offset+a.Length, b.Offset+b.Length
			if a.Offset >= be || b.Offset >= ae {
				continue
			}
			if !(a.Offset <= b.Offset && ae >= be || b.Offset <= a.Offset && be >= ae) {
				return invalid()
			}
			if a.Type == "code" || a.Type == "pre" || b.Type == "code" || b.Type == "pre" {
				return invalid()
			}
			if !styleEntity(a.Type) && !styleEntity(b.Type) {
				return invalid()
			}
		}
	}
	return nil
}

// TDLib represents date-time flags in tTdDw order, unlike the Bot API's
// documented w?[dD]?[tT]? grammar. Normalize unambiguous parser output at the
// wire boundary; reject conflicting short/long forms instead of guessing.
func wireDateTimeFormat(format string) (string, bool) {
	if dateTimeFormat.MatchString(format) {
		return format, true
	}
	week, date, clock := "", "", ""
	for _, r := range format {
		switch r {
		case 'w':
			if week != "" {
				return "", false
			}
			week = "w"
		case 'd', 'D':
			if date != "" {
				return "", false
			}
			date = string(r)
		case 't', 'T':
			if clock != "" {
				return "", false
			}
			clock = string(r)
		default:
			return "", false
		}
	}
	return week + date + clock, true
}

func styleEntity(kind string) bool {
	switch kind {
	case "bold", "italic", "underline", "strikethrough", "spoiler":
		return true
	}
	return false
}
