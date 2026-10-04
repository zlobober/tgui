// Copyright Aliaksei Levin (levlam@telegram.org), Arseny Smirnov (arseny30@gmail.com) 2014-2026
// Distributed under the Boost Software License, Version 1.0. See LICENSE_1_0.txt.
package markdownv2

import "testing"

func TestCheckedLink(t *testing.T) {
	cases := map[string]string{
		"EXAMPLE.COM":                 "http://example.com/",
		"https://EXAMPLE.COM:00443?x": "https://example.com:443/?x",
		"example.com/a b\n":           "http://example.com/a%20b",
		"example.com/\x00x":           "http://example.com/%00x",
		"example.com/\x00":            "http://example.com/",
		"example.com/\f":              "http://example.com/%0C",
		"u:p@example.com":             "http://u:p@example.com/",
		"[2001:db8::1]":               "http://[2001:db8::1]/",
		"TG:resolve?domain=Example":   "tg://resolve?domain=Example",
		"ton://transfer/a":            "ton://transfer/a",
		"tonsite://EXAMPLE.TON":       "tonsite://example.ton/",
		"ftp://example.com":           "", "example": "", "example.com:0": "", "example.com:65536": "",
		"exa%mple.com": "", "example.com:01": "http://example.com:1/", "tg://user:80": "",
		"tg://http://example.com": "", "tg://example.com": "", "[xyz::1]": "",
	}
	for source, want := range cases {
		if got := checkedLink(source); got != want {
			t.Errorf("%q got %q want %q", source, got, want)
		}
	}
}
func TestSpecialLinks(t *testing.T) {
	for _, s := range []string{"tg://emoji?id=0", "tg://emoji?id=01", "tg://emoji?id=+1", "tg://emoji?id=9223372036854775808", "tg://emoji?ID=1"} {
		if linkEmojiID(s) != "" {
			t.Errorf("accepted %s", s)
		}
	}
	if linkEmojiID("tg://emoji?id=-9223372036854775808") != "-9223372036854775808" {
		t.Fatal("signed emoji ID")
	}
	if linkUserID("TG:USER?ID=1099511627775") != 1099511627775 || linkUserID("tg:user?id=1099511627776") != 0 {
		t.Fatal("user ID bounds")
	}
	for _, s := range []string{"tg:time?unix=0", "tg:time?unix=2147483648", "tg:time?unix=1&format=rt", "tg:time?unix=1&format=ts", "tg:time?unix=1&unix=0"} {
		if _, _, ok := linkDate(s); ok {
			t.Errorf("accepted %s", s)
		}
	}
	n, format, ok := linkDate("tg:time?unix=1&unix=2&format=tttTTdDwW")
	if !ok || n != 2 || format != "tTdDw" {
		t.Fatalf("date: %d %q %v", n, format, ok)
	}
}
func TestErrorByteOffsets(t *testing.T) {
	for source, want := range map[string]int{"🏟*x": 4, "a!": 1, "[a](": 4, "![a]": -1, "\xff": -1} {
		_, err := Parse(source)
		pe, ok := err.(*ParseError)
		if !ok || pe.ByteOffset != want || pe.Code != 400 {
			t.Fatalf("%q: %#v", source, err)
		}
	}
}
func TestWhitespaceAndNUL(t *testing.T) {
	for _, source := range []string{"```go\x00x```", "```go\vx```"} {
		r, err := Parse(source)
		if err != nil || len(r.Entities) != 1 || r.Entities[0].Language != "go" {
			t.Fatalf("%q: %#v %v", source, r, err)
		}
	}
	r, err := Parse("```go\fx```")
	if err != nil || len(r.Entities) != 1 || r.Entities[0].Language != "" || r.Text != "go\fx" {
		t.Fatalf("formfeed: %#v %v", r, err)
	}
}
