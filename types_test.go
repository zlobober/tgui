package tgui

import (
	"strings"
	"testing"
)

func TestTextBuildersAndMarkdown(t *testing.T) {
	got := Join(Plain("😀 "), Bold("Milk"), Plain(" "), Code("2"))
	if got.Content != "😀 Milk 2" || len(got.Entities) != 2 || got.Entities[0].Offset != 3 || got.Entities[0].Length != 4 || got.Entities[1].Offset != 8 {
		t.Fatalf("wrong UTF16 entities: %+v", got)
	}
	if _, err := Split(got, 5); err != nil {
		t.Fatal(err)
	}
	doc, err := Markdown("*Milk* \\! `2`")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Content != "Milk ! 2" || len(doc.Entities) != 2 {
		t.Fatal(doc)
	}
	if d := Validate("Hello."); len(d) != 1 || d[0].ByteOffset != 5 {
		t.Fatalf("missing diagnostic: %+v", d)
	}
	if d := Validate("Hello\\."); len(d) != 0 {
		t.Fatal(d)
	}
	if _, err := Markdown(strings.Repeat("a", maxTextBytes+1)); err == nil {
		t.Fatal("unbounded Markdown")
	}
}

func TestFormattingInstructionEscapes(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`Цена — 156,06 евро\. Ещё…`, "Цена — 156,06 евро. Ещё…"},
		{`*Total* \- 156,06\.`, "Total - 156,06."},
		{`Path: C:\\tmp`, `Path: C:\tmp`},
		// The prompt warns about this; preserve TDLib's literal behavior.
		{`Цена \— 156,06 евро`, `Цена \— 156,06 евро`},
	} {
		got, err := Markdown(tc.source)
		if err != nil || got.Content != tc.want {
			t.Fatalf("%q: got %+v, %v; want %q", tc.source, got, err, tc.want)
		}
	}
}

func TestDateFormatTransportOrder(t *testing.T) {
	text, err := Markdown("![date](tg://time?unix=25&format=dt)")
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Entities) != 1 || text.Entities[0].DateTimeFormat != "dt" {
		t.Fatalf("incorrect date format: %+v", text)
	}
}

func TestSplitAtomicEmoji(t *testing.T) {
	text := Text{Content: "ab👍🏽z", Entities: []Entity{{Type: "custom_emoji", Offset: 2, Length: 4, CustomEmojiID: "25"}}}
	chunks, err := Split(text, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Content != "ab" || chunks[1].Content != "👍🏽z" || chunks[1].Entities[0].Length != 4 {
		t.Fatalf("split an atomic emoji: %+v", chunks)
	}
	if _, err = Split(text, 3); err == nil {
		t.Fatal("accepted limit smaller than atomic entity")
	}
}

func TestValidationIncludesRendererConstraints(t *testing.T) {
	// TDLib's low-level parser accepts code nested inside bold, but the Bot API
	// entity nesting contract does not. Public validation must catch this before send.
	source := "*`code`*"
	if _, err := Markdown(source); err == nil {
		t.Fatal("accepted unsendable nested code")
	}
	if len(Validate(source)) == 0 {
		t.Fatal("no diagnostic for renderer constraint")
	}
}
