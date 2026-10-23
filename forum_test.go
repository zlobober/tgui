package tgui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendForumTopicAndIncomingMetadata(t *testing.T) {
	var sent int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":8,"message":{"message_id":4,"from":{"id":42},"chat":{"id":-100123,"type":"supergroup"},"message_thread_id":7,"date":1700000000,"sender_chat":{"id":-99},"text":"hello"}}]}`)
			return
		}
		sent++
		if payload["message_thread_id"] != float64(7) {
			t.Error("missing topic on chunk")
		}
		preview, _ := payload["link_preview_options"].(map[string]any)
		if preview["is_disabled"] != true {
			t.Error("link preview not disabled")
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":-100123}}}`, sent)
	}))
	defer server.Close()
	client, err := NewClient("TEST", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := client.Send(context.Background(), -100123, Message{
		Text: Plain(strings.Repeat("😀", 3000)), ThreadID: 7, DisableLinkPreview: true,
	})
	if err != nil || len(refs) != 2 || sent != 2 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	updates, err := client.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := updates[0].Message
	if m.MessageThreadID != 7 || m.Date != 1700000000 || m.SenderChat.ID != -99 {
		t.Fatal(m)
	}
}

func TestCallEscapeHatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTEST/createForumTopic" {
			t.Error("unexpected endpoint")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_thread_id":7}}`)
	}))
	defer server.Close()
	client, err := NewClient("TEST", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var topic struct {
		ID int64 `json:"message_thread_id"`
	}
	if err := client.Call(context.Background(), "createForumTopic", map[string]any{"chat_id": -100123, "name": "test"}, &topic); err != nil {
		t.Fatal(err)
	}
	if topic.ID != 7 {
		t.Fatal(topic)
	}
	for _, method := range []string{"", "../getMe", "getMe?token=SECRET", "getMe/", "GET ME"} {
		if err := client.Call(context.Background(), method, nil, nil); err == nil {
			t.Fatalf("accepted method %q", method)
		}
	}
}
