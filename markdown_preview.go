package tgui

import (
	"strings"
	"unicode/utf8"
)

const (
	maxPreviewSourceBytes   = 64 * 1024
	maxPreviewParseAttempts = 512
)

// Search from the end, never inside a UTF-8 character. This deliberately uses
// the existing strict parser, not a second approximate Markdown grammar.
func markdownPreview(source string, limit int) (Text, bool) {
	if len(source) > maxPreviewSourceBytes || !utf8.ValidString(source) {
		return Text{}, false
	}
	end := len(source)
	for attempts := 0; end > 0 && attempts < maxPreviewParseAttempts; attempts++ {
		// A lone final backslash is parseable but can escape the next delta.
		slashes := 0
		for i := end - 1; i >= 0 && source[i] == '\\'; i-- {
			slashes++
		}
		if slashes%2 == 0 {
			if text, err := Markdown(source[:end]); err == nil && strings.TrimSpace(text.Content) != "" {
				return formattedPreviewTail(text, limit), true
			}
		}
		_, size := utf8.DecodeLastRuneInString(source[:end])
		end -= size
	}
	return Text{}, false
}

// Crop rendered text, never Markdown source; clip/rebase entities while keeping
// custom emoji and dates atomic. The ellipsis is outside all formatting spans.
func formattedPreviewTail(text Text, limit int) Text {
	total := UTF16Len(text.Content)
	if total <= limit {
		return text
	}
	start := total - (limit - 1)
	for _, e := range text.Entities {
		if (e.Type == "custom_emoji" || e.Type == "date_time") && e.Offset < start && start < e.Offset+e.Length {
			start = e.Offset + e.Length
		}
	}
	byteStart, units := len(text.Content), 0
	for i, r := range text.Content {
		if units >= start {
			byteStart = i
			break
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	start = units
	out := Text{Content: "…" + text.Content[byteStart:]}
	for _, e := range text.Entities {
		lo, hi := max(e.Offset, start), e.Offset+e.Length
		if lo < hi {
			e.Offset = lo - start + 1
			e.Length = hi - lo
			out.Entities = append(out.Entities, e)
		}
	}
	return out
}
