package tgui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitUnicodeAndEntities(t *testing.T) {
	text := Text{Content: "a😀bc𐀀d", Entities: []Entity{{Type: "bold", Offset: 1, Length: 6}, {Type: "italic", Offset: 3, Length: 2}}}
	got, err := Split(text, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []Text{
		{Content: "a😀", Entities: []Entity{{Type: "bold", Offset: 1, Length: 2}}},
		{Content: "bc", Entities: []Entity{{Type: "bold", Offset: 0, Length: 2}, {Type: "italic", Offset: 0, Length: 2}}},
		{Content: "𐀀d", Entities: []Entity{{Type: "bold", Offset: 0, Length: 2}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	if text.Entities[0].Offset != 1 || text.Entities[0].Length != 6 {
		t.Fatal("mutated input")
	}
}
func TestSplitPreservesEveryByte(t *testing.T) {
	for _, content := range []string{"", " \n\t  hello\n", "e\u0301👨‍👩‍👧‍👦🇳🇱", strings.Repeat("abc😀\n", 2000)} {
		for _, limit := range []int{2, 3, 17, 3500, 4096} {
			parts, err := Split(Plain(content), limit)
			if err != nil {
				t.Fatal(err)
			}
			var joined strings.Builder
			for _, p := range parts {
				if !utf8.ValidString(p.Content) || UTF16Len(p.Content) > limit || p.Content == "" {
					t.Fatalf("invalid chunk: %q", p.Content)
				}
				joined.WriteString(p.Content)
			}
			if joined.String() != content {
				t.Fatal("lost text")
			}
		}
	}
}
func TestSplitRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		text  Text
		limit int
	}{
		{"limit", Plain("a"), 0},
		{"negative", Plain("a"), -1},
		{"surrogate", Plain("😀"), 1},
		{"utf8", Plain("\xff"), 8},
		{"oversized", Plain(strings.Repeat("a", maxTextBytes+1)), 3500},
		{"unknown", Text{"abc", []Entity{{Type: "wat", Length: 1}}}, 3},
		{"zero", Text{"abc", []Entity{{Type: "bold", Length: 0}}}, 3},
		{"negative offset", Text{"abc", []Entity{{Type: "bold", Offset: -1, Length: 1}}}, 3},
		{"range", Text{"abc", []Entity{{Type: "bold", Length: 4}}}, 3},
		{"overflow", Text{"abc", []Entity{{Type: "bold", Offset: 1, Length: int(^uint(0) >> 1)}}}, 3},
		{"surrogate start", Text{"😀a", []Entity{{Type: "bold", Offset: 1, Length: 2}}}, 3},
		{"surrogate end", Text{"😀a", []Entity{{Type: "bold", Length: 1}}}, 3},
		{"partial overlap", Text{"abcd", []Entity{{Type: "bold", Length: 3}, {Type: "italic", Offset: 1, Length: 3}}}, 4},
		{"code nested", Text{"abcd", []Entity{{Type: "code", Length: 4}, {Type: "italic", Offset: 1, Length: 2}}}, 4},
		{"quotes nested", Text{"abcd", []Entity{{Type: "blockquote", Length: 4}, {Type: "expandable_blockquote", Offset: 1, Length: 2}}}, 4},
		{"links nested", Text{"abcd", []Entity{{Type: "text_link", Length: 4, URL: "https://example.org"}, {Type: "text_link", Offset: 1, Length: 2, URL: "https://example.org"}}}, 4},
		{"wrong field", Text{"abc", []Entity{{Type: "bold", Length: 1, URL: "https://example.org"}}}, 3},
		{"missing URL", Text{"abc", []Entity{{Type: "text_link", Length: 1}}}, 3},
		{"unsafe URL", Text{"abc", []Entity{{Type: "text_link", Length: 1, URL: "javascript:alert(1)"}}}, 3},
		{"user", Text{"abc", []Entity{{Type: "text_mention", Length: 1}}}, 3},
		{"emoji", Text{"abc", []Entity{{Type: "custom_emoji", Length: 1, CustomEmojiID: "abc"}}}, 3},
		{"date format", Text{"abc", []Entity{{Type: "date_time", Length: 1, UnixTime: 123, DateTimeFormat: "rw"}}}, 3},
		{"language injection", Text{"abc", []Entity{{Type: "pre", Length: 1, Language: "go\n"}}}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Split(tc.text, tc.limit); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
func TestSplitMetadataPreserved(t *testing.T) {
	entities := []Entity{
		{Type: "text_link", Length: 4, URL: "https://example.org"},
		{Type: "text_mention", Length: 4, UserID: 42},
		{Type: "pre", Length: 4, Language: "go"},
		{Type: "date_time", Length: 4, UnixTime: 12345, DateTimeFormat: "wDT"},
	}
	for _, e := range entities {
		if e.Type == "date_time" {
			if _, err := Split(Text{Content: "abcd", Entities: []Entity{e}}, 2); err == nil {
				t.Fatal("date would be duplicated across chunks")
			}
			parts, err := Split(Text{Content: "abcd", Entities: []Entity{e}}, 4)
			if err != nil || len(parts) != 1 || !reflect.DeepEqual(parts[0].Entities, []Entity{e}) {
				t.Fatalf("lost atomic date metadata: %#v %v", parts, err)
			}
			continue
		}
		parts, err := Split(Text{Content: "abcd", Entities: []Entity{e}}, 2)
		if err != nil {
			t.Fatal(err)
		}
		e.Length = 2
		if len(parts) != 2 || !reflect.DeepEqual(parts[0].Entities, []Entity{e}) || !reflect.DeepEqual(parts[1].Entities, []Entity{e}) {
			t.Fatalf("lost metadata: %#v", parts)
		}
	}
}
func FuzzSplitPreservesText(f *testing.F) {
	f.Add("hello😀 world", uint16(7))
	f.Add("e\u0301\n", uint16(2))
	f.Fuzz(func(t *testing.T, s string, size uint16) {
		if !utf8.ValidString(s) || len(s) > maxTextBytes {
			return
		}
		limit := int(size%4095) + 2
		parts, err := Split(Plain(s), limit)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, p := range parts {
			if UTF16Len(p.Content) > limit || !utf8.ValidString(p.Content) {
				t.Fatal("invalid chunk")
			}
			out.WriteString(p.Content)
		}
		if out.String() != s {
			t.Fatal("text changed")
		}
	})
}
