// Copyright Aliaksei Levin (levlam@telegram.org), Arseny Smirnov (arseny30@gmail.com) 2014-2026
// Distributed under the Boost Software License, Version 1.0. See LICENSE_1_0.txt.
package markdownv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUpstream(t *testing.T) {
	data, err := os.ReadFile("testdata/upstream.json")
	if err != nil {
		t.Fatal(err)
	}
	// UserID intentionally isn't a transport JSON field, but is part of the fixtures.
	type fixtureEntity struct {
		Entity
		UserID int64 `json:"user_id"`
	}
	var cases []struct {
		Source, Text, Error string
		Entities            []fixtureEntity
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 121 {
		t.Fatal("missing upstream cases")
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := Parse(c.Source)
			if c.Error != "" {
				var pe *ParseError
				if !errors.As(err, &pe) || pe.Code != 400 || pe.Message != c.Error {
					t.Fatalf("source %q: error %v; want %s", c.Source, err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := make([]Entity, 0, len(c.Entities))
			for _, e := range c.Entities {
				e.Entity.UserID = e.UserID
				want = append(want, e.Entity)
			}
			if got.Text != c.Text || !reflect.DeepEqual(got.Entities, want) {
				t.Fatalf("source %q\ngot %#v\nwant text %q entities %#v", c.Source, got, c.Text, want)
			}
		})
	}
}

// This is the complete active ASCII loop from the upstream parse_markdown test.
func TestUpstreamASCII(t *testing.T) {
	for c := byte(1); c < 126; c++ {
		if strings.ContainsRune("_*[~`>", rune(c)) {
			continue
		}
		source := string(c)
		got, err := Parse(source)
		if !strings.ContainsRune("]()>#+-=|{}.!", rune(c)) {
			if err != nil || got.Text != source || len(got.Entities) != 0 {
				t.Fatalf("%q: %#v %v", source, got, err)
			}
		} else {
			want := fmt.Sprintf("Character '%c' is reserved and must be escaped with the preceding '\\'", c)
			if err == nil || err.Error() != want {
				t.Fatalf("%q: %v", source, err)
			}
			got, err = Parse("\\" + source)
			if err != nil || got.Text != source || len(got.Entities) != 0 {
				t.Fatalf("escaped %q: %#v %v", source, got, err)
			}
		}
	}
}

func checkInvariants(t *testing.T, s string) {
	t.Helper()
	r, err := Parse(s)
	if err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Code != 400 || pe.ByteOffset < -1 || pe.ByteOffset > len(s) {
			t.Fatalf("invalid error: %#v", err)
		}
		return
	}
	if !utf8.ValidString(s) || !utf8.ValidString(r.Text) {
		t.Fatal("invalid UTF-8 accepted/produced")
	}
	n := 0
	for _, c := range r.Text {
		n++
		if c > 0xffff {
			n++
		}
	}
	for _, e := range r.Entities {
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > n {
			t.Fatalf("invalid entity %#v in %q", e, r.Text)
		}
	}
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
}
func TestRandomUTF8(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	alphabet := []rune("abc🏟➡️\r\n\x00 _*[]()~`>#+-=|{}.!\\")
	for i := 0; i < 20000; i++ {
		v := make([]rune, r.Intn(160))
		for j := range v {
			v[j] = alphabet[r.Intn(len(alphabet))]
		}
		checkInvariants(t, string(v))
	}
	for _, s := range []string{"\xff", "a\xc0\x80", "\xed\xa0\x80"} {
		if Validate(s) == nil {
			t.Fatal("invalid UTF8 accepted")
		}
	}
}
func FuzzParse(f *testing.F) {
	for _, s := range []string{"", ">||", "*bold*", "![x](tg://emoji?id=1)", "```go\nx```", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		checkInvariants(t, s)
	})
}
