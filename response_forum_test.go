package tgui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestForumResponsePreviewAndFinal(t *testing.T) {
	for _, mode := range []PreviewMode{PreviewAuto, PreviewDraft, PreviewEdit} {
		f := &responseFake{}
		r, now := responseWithClock(f, ResponseOptions{Mode: mode, ThreadID: 7, DisableLinkPreview: true, MinInterval: time.Second})
		r.Update(Plain("first line"))
		if err := r.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		r.Update(Plain("first line\nsecond line"))
		*now = now.Add(time.Second)
		if err := r.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		final := strings.Repeat("line 😀\n", 1000)
		refs, err := r.Finish(context.Background(), Message{Text: Plain(final)})
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) < 2 || f.calls[0].kind != "send" || f.calls[1].kind != "edit" || f.calls[2].kind != "edit" {
			t.Fatal(f.calls)
		}
		var delivered string
		for i, call := range f.calls {
			if call.kind == "draft" || call.message.ThreadID != 7 || !call.message.DisableLinkPreview {
				t.Fatal(call)
			}
			if i >= 2 {
				delivered += call.message.Text.Content
			}
		}
		if delivered != final {
			t.Fatal("final changed or truncated")
		}
		count := len(f.calls)
		if _, err := r.Finish(context.Background(), Message{Text: Plain(final)}); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != count {
			t.Fatal("duplicate final delivery")
		}
	}
}

func TestShortForumResponseEditsInsteadOfDuplicating(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, -100123, ResponseOptions{ThreadID: 9})
	r.Update(Plain("Hello"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	refs, err := r.Finish(context.Background(), Message{Text: Plain("Hello world")})
	if err != nil || len(refs) != 1 || len(f.calls) != 2 || f.calls[1].kind != "edit" {
		t.Fatalf("refs=%v err=%v calls=%v", refs, err, f.calls)
	}
}
