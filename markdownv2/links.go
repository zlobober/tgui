// Copyright Aliaksei Levin (levlam@telegram.org), Arseny Smirnov (arseny30@gmail.com) 2014-2026
// Distributed under the Boost Software License, Version 1.0. See LICENSE_1_0.txt.
package markdownv2

import (
	"net"
	"strconv"
	"strings"
)

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
func integer(s string, bits int) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, bits)
	return n, err == nil && strconv.FormatInt(n, 10) == s
}
func tgQuery(s, host string) (string, bool) {
	if !strings.HasPrefix(asciiLower(s), "tg:") {
		return "", false
	}
	s = s[3:]
	s = strings.TrimPrefix(s, "//")
	if len(s) < len(host) || asciiLower(s[:len(host)]) != host {
		return "", false
	}
	s = s[len(host):]
	s = strings.TrimPrefix(s, "/")
	if !strings.HasPrefix(s, "?") {
		return "", false
	}
	s = s[1:]
	s, _, _ = strings.Cut(s, "#")
	return s, true
}
func linkUserID(s string) int64 {
	q, ok := tgQuery(asciiLower(s), "user")
	if !ok {
		return 0
	}
	for _, p := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(p, "=")
		if k == "id" {
			n, ok := integer(v, 64)
			if ok && n > 0 && n < (1<<40) {
				return n
			}
			return 0
		}
	}
	return 0
}
func linkEmojiID(s string) string {
	q, ok := tgQuery(s, "emoji")
	if !ok {
		return ""
	}
	for _, p := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(p, "=")
		if k == "id" {
			n, ok := integer(v, 64)
			if ok && n != 0 {
				return v
			}
			return ""
		}
	}
	return ""
}
func linkDate(s string) (int64, string, bool) {
	q, ok := tgQuery(s, "time")
	if !ok {
		return 0, "", false
	}
	var date int64
	format := ""
	for _, p := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(p, "=")
		if k == "unix" {
			date, ok = integer(v, 32)
			if !ok || date <= 0 {
				return 0, "", false
			}
		}
		if k == "format" {
			format = v
		}
	}
	if date == 0 {
		return 0, "", false
	}
	if format == "r" || format == "R" {
		return date, "r", true
	}
	for _, c := range format {
		if !strings.ContainsRune("tTdDwW", c) {
			return 0, "", false
		}
	}
	canonical := ""
	for _, c := range "tTdDw" {
		if strings.ContainsRune(format, c) || (c == 'w' && strings.ContainsRune(format, 'W')) {
			canonical += string(c)
		}
	}
	return date, canonical, true
}
func alnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
func hex(c byte) bool   { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func validURLPart(s string, colon bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if alnum(c) || c >= 128 || strings.ContainsRune(".-_!$,~*'();&+=", rune(c)) || colon && c == ':' {
			continue
		}
		if c == '%' && i+2 < len(s) && hex(s[i+1]) && hex(s[i+2]) {
			i += 2
			continue
		}
		return false
	}
	return true
}

// checkedLink ports LinkManager::check_link_impl and HttpUrl::parse_url.
func checkedLink(s string) string {
	special := ""
	for _, scheme := range []string{"tg", "ton", "tonsite"} {
		if strings.HasPrefix(asciiLower(s), scheme+":") {
			special = scheme
			s = s[len(scheme)+1:]
			s = strings.TrimPrefix(s, "//")
			break
		}
	}
	protocol := "http"
	explicit := false
	end := strings.IndexAny(s, ":/?#@[]")
	if end >= 0 && strings.HasPrefix(s[end:], "://") {
		protocol = asciiLower(s[:end])
		if protocol != "http" && protocol != "https" {
			return ""
		}
		s = s[end+3:]
		explicit = true
	}
	authority, query := s, ""
	if n := strings.IndexAny(s, "/?#"); n >= 0 {
		authority, query = s[:n], s[n:]
	}
	port := 0
	colon := len(authority) - 1
	for colon > 0 && authority[colon] != ':' && authority[colon] != ']' && authority[colon] != '@' {
		colon--
	}
	if colon > 0 && authority[colon] == ':' {
		p := authority[colon+1:]
		for len(p) > 1 && p[0] == '0' {
			p = p[1:]
		}
		n, ok := integer(p, 32)
		if !ok || n <= 0 || n > 65535 {
			return ""
		}
		port = int(n)
		authority = authority[:colon]
	}
	user, host := "", authority
	if n := strings.LastIndexByte(authority, '@'); n >= 0 {
		user, host = authority[:n], authority[n+1:]
	}
	if host == "" || host == "." {
		return ""
	}
	host = asciiLower(host)
	ipv6 := strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	if ipv6 {
		if net.ParseIP(host[1:len(host)-1]) == nil || !strings.Contains(host, ":") {
			return ""
		}
		for i := 1; i+1 < len(host); i++ {
			if !strings.ContainsRune(":0123456789abcdef.", rune(host[i])) {
				return ""
			}
		}
	} else if !validURLPart(host, false) || !validURLPart(user, true) {
		return ""
	}
	for len(query) > 0 && space(query[len(query)-1]) {
		query = query[:len(query)-1]
	}
	if query == "" {
		query = "/"
	}
	if query[0] != '/' {
		query = "/" + query
	}
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c <= 32 {
			b.WriteByte('%')
			b.WriteByte("0123456789ABCDEF"[c/16])
			b.WriteByte("0123456789ABCDEF"[c%16])
		} else {
			b.WriteByte(c)
		}
	}
	query = b.String()
	if special != "" {
		if explicit || user != "" || port != 0 || ipv6 {
			return ""
		}
		for i := 0; i < len(host); i++ {
			c := host[i]
			if !alnum(c) && c != '-' && c != '_' && !(special == "tonsite" && c == '.') {
				return ""
			}
		}
		if strings.HasPrefix(query, "/?") {
			query = query[1:]
		}
		return special + "://" + host + query
	}
	if !strings.Contains(host, ".") && !ipv6 {
		return ""
	}
	result := protocol + "://"
	if user != "" {
		result += user + "@"
	}
	result += host
	if port > 0 {
		result += ":" + strconv.Itoa(port)
	}
	return result + query
}
