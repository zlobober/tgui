package tgui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFormattedPreviewEntitiesAndOwnership(t *testing.T) {
	f := &responseFake{}
	r, now := responseWithClock(f, ResponseOptions{ThreadID: 7, MinInterval: time.Second, MaxPreviewUnits: 20})
	text := Bold("😀 hello")
	if err := r.UpdateFormatted(text); err != nil {
		t.Fatal(err)
	}
	text.Entities[0].Length = 1 // Must not corrupt the copied preview.
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || len(f.calls[0].text.Entities) != 1 || f.calls[0].text.Entities[0].Length != 8 {
		t.Fatal(f.calls)
	}
	*now = now.Add(time.Second)
	long := Join(Plain(strings.Repeat("prefix ", 10)), Bold("🦔 tail text"))
	if err := r.UpdateFormatted(long); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	preview := f.calls[1].text
	if UTF16Len(preview.Content) > 20 || !strings.HasPrefix(preview.Content, "…") || len(preview.Entities) != 1 {
		t.Fatal(preview)
	}
	if _, err := Split(preview, 20); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	r.Update(Bold("plain remains plain")) // Preserve old Update contract.
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls[2].text.Entities) != 0 {
		t.Fatal("Update unexpectedly preserved formatting")
	}
}
func TestFormattedPreviewRejectsInvalidEntities(t *testing.T) {
	f := &responseFake{}
	r := NewResponse(f, 42, ResponseOptions{})
	if err := r.UpdateFormatted(Text{Content: "😀", Entities: []Entity{{Type: "bold", Offset: 0, Length: 1}}}); err == nil {
		t.Fatal("accepted split surrogate")
	}
	if len(f.calls) != 0 {
		t.Fatal("validation performed IO")
	}
}
