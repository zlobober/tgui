package tgui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMarkdownPreviewLongestPrefix(t *testing.T) {
	for _, tc := range []struct {
		source, want string
		entities     int
	}{
		{"• [Milk](https://example.org/milk)\n• [Unfinished", "• Milk\n• ", 1},
		{"*Bold* and _unfinished", "Bold and ", 1},
		{`😀 \`, "😀 ", 0},
		{"`code` next ```go\nunclosed", "code next ", 1},
		{" ", "", 0},
	} {
		text, ok := markdownPreview(tc.source, 3500)
		if ok != (tc.want != "") || text.Content != tc.want || len(text.Entities) != tc.entities {
			t.Fatalf("%q: got %+v %t", tc.source, text, ok)
		}
	}
}

func TestMarkdownPreviewCarriesSuffixViaCumulativeUpdates(t *testing.T) {
	f := &responseFake{}
	r, now := responseWithClock(f, ResponseOptions{Mode: PreviewDraft})
	ctx := context.Background()
	sources := []string{"*Heading*\n[Mi", "*Heading*\n[Milk](https://example.org/m", "*Heading*\n[Milk](https://example.org/milk)"}
	for _, s := range sources {
		r.UpdateMarkdown(s)
		if err := r.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Second)
	}
	last := f.calls[len(f.calls)-1].text
	if last.Content != "Heading\nMilk" || len(last.Entities) != 2 || last.Entities[1].URL != "https://example.org/milk" {
		t.Fatalf("suffix/link lost: %+v", last)
	}
	for _, c := range f.calls {
		if strings.Contains(c.text.Content, "https:") || strings.Contains(c.text.Content, "[Mi") {
			t.Fatal("raw markup leaked", c)
		}
	}
}

func TestMarkdownPreviewBudgetAndEntityOnlyUpdates(t *testing.T) {
	f := &responseFake{}
	r, now := responseWithClock(f, ResponseOptions{Mode: PreviewDraft})
	for _, source := range []string{"word", "*word*", "[word](tg://user?id=123)", "[word](tg://user?id=456)"} {
		r.UpdateMarkdown(source)
		if err := r.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Second)
	}
	if len(f.calls) != 4 || f.calls[3].text.Entities[0].UserID != 456 {
		t.Fatal("entity changes ignored", f.calls)
	}
	count := len(f.calls)
	r.UpdateMarkdown("[" + strings.Repeat("x", maxPreviewParseAttempts+20))
	_ = r.Flush(context.Background())
	if len(f.calls) != count {
		t.Fatal("budget exhaustion overwrote last preview")
	}
	*now = now.Add(2 * time.Second)
	r.UpdateMarkdown(strings.Repeat("x", maxPreviewSourceBytes+1))
	_ = r.Flush(context.Background())
	if len(f.calls) != count {
		t.Fatal("oversized preview accepted")
	}
	*now = now.Add(2 * time.Second)
	r.Update(Plain("Working…"))
	_ = r.Flush(context.Background())
	if f.calls[len(f.calls)-1].text.Content != "Working…" {
		t.Fatal("cannot switch to plain progress")
	}
}

func TestFormattedPreviewTailPreservesEntities(t *testing.T) {
	text, err := Markdown("😀😀 [Milk](https://example.org) *bold*")
	if err != nil {
		t.Fatal(err)
	}
	tail := formattedPreviewTail(text, 10)
	if UTF16Len(tail.Content) > 10 || len(tail.Entities) != 2 {
		t.Fatalf("bad tail: %+v", tail)
	}
	if _, err := Split(tail, 10); err != nil {
		t.Fatal(err)
	}
	atomic := Text{Content: "ab👍🏽z", Entities: []Entity{{Type: "custom_emoji", Offset: 2, Length: 4, CustomEmojiID: "25"}}}
	if got := formattedPreviewTail(atomic, 4); got.Content != "…z" || len(got.Entities) != 0 {
		t.Fatal("split emoji", got)
	}
}

func TestMarkdownPreviewEditFallbackAndFinalRemainFormatted(t *testing.T) {
	f := &responseFake{}
	r, _ := responseWithClock(f, ResponseOptions{Mode: PreviewEdit})
	r.UpdateMarkdown("*Heading* unfinished [link")
	_ = r.Flush(context.Background())
	if len(f.calls) != 1 || len(f.calls[0].text.Entities) != 1 {
		t.Fatal("preview formatting lost")
	}
	final := Message{Text: Plain("Literal final [unfinished")}
	_, err := r.Finish(context.Background(), final)
	if err != nil || !reflect.DeepEqual(f.calls[len(f.calls)-1].text, final.Text) {
		t.Fatal("preview changed final", err)
	}
}
