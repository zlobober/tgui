package tgui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForumNotModifiedClassification(t *testing.T) {
	for _, tc := range []struct {
		method, description string
		code                int
		expected            bool
	}{
		{"editForumTopic", "Bad Request: TOPIC_NOT_MODIFIED", 400, true},
		{"editForumTopic", "Bad Request: TOPIC_ID_INVALID", 400, false},
		{"editForumTopic", "Bad Request: TOPIC_NOT_MODIFIED", 403, false},
		{"createForumTopic", "Bad Request: TOPIC_NOT_MODIFIED", 400, false},
	} {
		t.Run(fmt.Sprintf("%s/%d/%s", tc.method, tc.code, tc.description), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q}`, tc.code, tc.description)
			}))
			defer server.Close()
			client, err := NewClient("TEST", WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			err = client.Call(context.Background(), tc.method, struct{}{}, nil)
			var api *APIError
			if !errors.As(err, &api) || api.NotModified != tc.expected {
				t.Fatalf("error=%v expected NotModified=%t", err, tc.expected)
			}
		})
	}
}
