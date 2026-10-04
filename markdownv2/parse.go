// Copyright Aliaksei Levin (levlam@telegram.org), Arseny Smirnov (arseny30@gmail.com) 2014-2026
// Distributed under the Boost Software License, Version 1.0.
// See LICENSE_1_0.txt. Go port provenance is documented in README.md.

// Package markdownv2 parses Telegram MarkdownV2 without network access.
package markdownv2

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type Entity struct {
	Type           string `json:"type"`
	Offset         int    `json:"offset"`
	Length         int    `json:"length"`
	URL            string `json:"url,omitempty"`
	Language       string `json:"language,omitempty"`
	CustomEmojiID  string `json:"custom_emoji_id,omitempty"`
	UserID         int64  `json:"-"`
	UnixTime       int64  `json:"unix_time,omitempty"`
	DateTimeFormat string `json:"date_time_format,omitempty"`
}
type Result struct {
	Text     string   `json:"text"`
	Entities []Entity `json:"entities"`
}
type ParseError struct {
	Message    string
	ByteOffset int
	Code       int
}

func (e *ParseError) Error() string               { return e.Message }
func parseError(message string, offset int) error { return &ParseError{message, offset, 400} }
func Validate(source string) error                { _, err := Parse(source); return err }

type entityInfo struct {
	kind, argument            string
	offset, byteOffset, begin int
}

var names = map[string]string{"bold": "Bold", "italic": "Italic", "code": "Code", "pre": "Pre", "pre_code": "PreCode", "text_link": "TextUrl", "underline": "Underline", "strikethrough": "Strikethrough", "spoiler": "Spoiler", "custom_emoji": "CustomEmoji", "blockquote": "BlockQuote"}

func unclosed(e entityInfo) error {
	return parseError(fmt.Sprintf("Can't find end of %s entity at byte offset %d", names[e.kind], e.byteOffset), e.byteOffset)
}
func space(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\x00'
}

// Parse is a stack-machine port of TDLib's parse_markdown_v2. Offsets and lengths
// count UTF-16 code units; ParseError.ByteOffset counts source bytes.
func Parse(source string) (Result, error) {
	if !utf8.ValidString(source) {
		return Result{}, parseError("Text must be encoded in UTF-8", -1)
	}
	at := func(i int) byte {
		if i < 0 || i >= len(source) {
			return 0
		}
		return source[i]
	}
	out := make([]byte, 0, len(source))
	entities := make([]Entity, 0)
	stack := []entityInfo{}
	offset := 0
	haveQuote, canQuote := false, true
	fail := func(err error) (Result, error) { return Result{}, err }
	reservedError := func(c byte, i int) error {
		return parseError(fmt.Sprintf("Character '%c' is reserved and must be escaped with the preceding '\\'", c), i)
	}
	emit := func(info entityInfo, kind string, e Entity) {
		e.Type = kind
		e.Offset = info.offset
		e.Length = offset - info.offset
		if kind == "pre_code" {
			e.Type = "pre"
			e.Language = info.argument
		}
		entities = append(entities, e)
	}
	for i := 0; i < len(source); i++ {
		c := at(i)
		if c == '\\' && at(i+1) > 0 && at(i+1) <= 126 {
			i++
			offset++
			out = append(out, at(i))
			if at(i) != '\r' {
				canQuote = at(i) == '\n'
			}
			continue
		}
		reserved := "_*[]()~`>#+-=|{}.!\n"
		if len(stack) > 0 {
			switch stack[len(stack)-1].kind {
			case "code", "pre", "pre_code":
				reserved = "`"
			}
		}
		if !strings.ContainsRune(reserved, rune(c)) {
			if c&0xc0 != 0x80 {
				offset++
				if c >= 0xf0 {
					offset++
				}
				if c != '\r' {
					canQuote = false
				}
			}
			out = append(out, c)
			continue
		}
		end := false
		if len(stack) > 0 {
			if haveQuote && c == '\n' && (i+1 == len(source) || at(i+1) != '>') {
				end = true
			} else {
				switch stack[len(stack)-1].kind {
				case "bold":
					end = c == '*'
				case "italic":
					end = c == '_' && at(i+1) != '_'
				case "code":
					end = c == '`'
				case "pre", "pre_code":
					end = c == '`' && at(i+1) == '`' && at(i+2) == '`'
				case "text_link", "custom_emoji":
					end = c == ']'
				case "underline":
					end = c == '_' && at(i+1) == '_'
				case "strikethrough":
					end = c == '~'
				case "spoiler":
					end = c == '|' && at(i+1) == '|'
				}
			}
		}
		if !end {
			kind, arg := "", ""
			start := i
			switch c {
			case '_':
				kind = "italic"
				if at(i+1) == '_' {
					kind = "underline"
					i++
				}
			case '*':
				kind = "bold"
			case '~':
				kind = "strikethrough"
			case '|':
				if at(i+1) != '|' {
					return fail(reservedError(c, i))
				}
				i++
				kind = "spoiler"
			case '[':
				kind = "text_link"
			case '`':
				kind = "code"
				if at(i+1) == '`' && at(i+2) == '`' {
					i += 3
					kind = "pre"
					j := i
					for j < len(source) && !space(at(j)) && at(j) != '`' {
						j++
					}
					if i != j && j < len(source) && at(j) != '`' {
						kind = "pre_code"
						arg = source[i:j]
						i = j
					}
					if at(i) == '\n' || at(i) == '\r' {
						if (at(i+1) == '\n' || at(i+1) == '\r') && at(i) != at(i+1) {
							i += 2
						} else {
							i++
						}
					}
					i--
				}
			case '!':
				if at(i+1) != '[' {
					return fail(reservedError(c, i))
				}
				i++
				kind = "custom_emoji"
			case '\n':
				offset++
				out = append(out, c)
				canQuote = true
				continue
			case '>':
				if !canQuote {
					return fail(reservedError(c, i))
				}
				if haveQuote {
					continue
				}
				kind = "blockquote"
				haveQuote = true
			default:
				return fail(reservedError(c, i))
			}
			stack = append(stack, entityInfo{kind, arg, offset, start, len(out)})
		} else {
			info := stack[len(stack)-1]
			kind := info.kind
			if c == '\n' && kind != "blockquote" {
				if kind != "spoiler" || !(info.byteOffset == i-2 || (info.byteOffset == i-3 && len(out) > 0 && out[len(out)-1] == '\r')) {
					return fail(unclosed(info))
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return fail(unclosed(info))
				}
				info = stack[len(stack)-1]
				if info.kind != "blockquote" {
					return fail(unclosed(info))
				}
				kind = "expandable_blockquote"
			}
			skip := offset == info.offset
			e := Entity{}
			switch kind {
			case "underline", "spoiler":
				i++
			case "pre", "pre_code":
				i += 2
			case "text_link", "custom_emoji":
				link := ""
				if at(i+1) != '(' {
					if kind == "custom_emoji" {
						return fail(parseError("The entity must contain a tg://emoji or tg://time URL", -1))
					}
					link = string(out[info.begin:])
				} else {
					i += 2
					start := i
					var b strings.Builder
					for i < len(source) && at(i) != ')' {
						if at(i) == '\\' && at(i+1) > 0 && at(i+1) <= 126 {
							b.WriteByte(at(i + 1))
							i += 2
						} else {
							b.WriteByte(at(i))
							i++
						}
					}
					if at(i) != ')' {
						return fail(parseError(fmt.Sprintf("Can't find end of a URL at byte offset %d", start), start))
					}
					link = b.String()
				}
				if kind == "text_link" {
					if id := linkUserID(link); id != 0 {
						kind = "text_mention"
						e.UserID = id
					} else {
						e.URL = checkedLink(link)
						if e.URL == "" {
							skip = true
						}
					}
				} else {
					if id := linkEmojiID(link); id != "" {
						e.CustomEmojiID = id
					} else if date, format, ok := linkDate(link); ok {
						kind = "date_time"
						e.UnixTime = date
						e.DateTimeFormat = format
					} else {
						return fail(parseError("Invalid tg://emoji or tg://time URL specified", -1))
					}
				}
			case "blockquote", "expandable_blockquote":
				haveQuote = false
				out = append(out, c)
				canQuote = true
				offset++
				skip = false
			}
			if !skip {
				emit(info, kind, e)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if haveQuote && len(stack) > 0 {
		kind := "blockquote"
		if stack[len(stack)-1].kind == "spoiler" && stack[len(stack)-1].byteOffset == len(source)-2 {
			stack = stack[:len(stack)-1]
			kind = "expandable_blockquote"
		}
		if len(stack) > 0 && stack[len(stack)-1].kind == "blockquote" {
			info := stack[len(stack)-1]
			if offset != info.offset {
				emit(info, kind, Entity{})
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		return fail(unclosed(stack[len(stack)-1]))
	}
	priority := func(e Entity) int {
		switch e.Type {
		case "blockquote", "expandable_blockquote":
			return 0
		case "pre":
			if e.Language != "" {
				return 10
			}
			return 11
		case "code":
			return 20
		case "date_time":
			return 30
		case "text_link", "text_mention":
			return 49
		case "bold":
			return 90
		case "italic":
			return 91
		case "underline":
			return 92
		case "strikethrough":
			return 93
		case "spoiler":
			return 94
		default:
			return 99
		}
	}
	sort.SliceStable(entities, func(i, j int) bool {
		a, b := entities[i], entities[j]
		if a.Offset != b.Offset {
			return a.Offset < b.Offset
		}
		if a.Length != b.Length {
			return a.Length > b.Length
		}
		return priority(a) < priority(b)
	})
	return Result{string(out), entities}, nil
}

func FormattingInstructions() string {
	return "Write Telegram MarkdownV2, not CommonMark or HTML: *bold*, _italic_, __underline__, ~strike~, ||spoiler||, `code`, ```language\ncode```, [label](https://example.com). Prefix quote lines with >. Match delimiters; never nest code with other formatting. No tables or headings. For lists use • bullets or escaped numbering (1\\.).\n" +
		"Outside code, prefix literal _ * [ ] ( ) ~ ` > # + - = | { } . ! with a backslash. Write a literal backslash as \\\\. Inside code, escape only backticks and backslashes; inside link URLs, escape ) and backslashes. Only ASCII characters can be escaped; write Unicode punctuation (—, …) unchanged. A backslash before Unicode remains visible.\n" +
		"Replies are parsed locally into text and formatting entities. Do not wrap the whole reply in a code fence unless it is code."
}
