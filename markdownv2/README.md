# MarkdownV2 parser

Pure-Go, offline port of TDLib's `parse_markdown_v2`, pinned to
[`42e6a5259551178d1dab54a22ad96d14bd906e20`](https://github.com/tdlib/td/tree/42e6a5259551178d1dab54a22ad96d14bd906e20).
No regex parser, subprocess, C++ runtime, network request, or external Go dependency.

## Provenance and license

The parser and tests are derived from TDLib, copyright Aliaksei Levin
(levlam@telegram.org), Arseny Smirnov (arseny30@gmail.com) 2014–2026,
distributed under the Boost Software License 1.0. The unmodified license is
in `LICENSE_1_0.txt`; copyright/license notices are retained in the port.

Upstream files at the pinned revision:

- `td/telegram/MessageEntity.cpp`: `parse_markdown_v2`, entity priorities/names.
- `td/telegram/MessageEntity.h`: entity ordering.
- `td/telegram/LinkManager.cpp`: `check_link_impl`, user/emoji/date link helpers.
- `td/telegram/FormattedDate.cpp`: date format parsing.
- `td/telegram/UserId.h`: valid user ID range.
- `tdutils/td/utils/HttpUrl.cpp`: URL parsing/normalization.
- `tdutils/td/utils/misc.h`: ASCII whitespace and canonical integer checks.
- `test/message_entities.cpp` (local snapshot `message_entities_test.cpp`):
  **all 121 active literal calls** in `TEST(MessageEntities, parse_markdown)`
  are translated to `testdata/upstream.json`. The two commented-out calls
  remain excluded, just as upstream. `TestUpstreamASCII` ports the entire
  active ASCII loop. Both upstream overloads actually call v2, not v1.

## Contract

`Parse(source)` returns stripped text and sorted entities; offsets/lengths
are **UTF-16 code units**. `Validate` runs precisely the same parser.
`FormattingInstructions` supplies a concise grammar/escaping reference.
All state is per invocation; results do not alias input or shared state.

Entity names use Bot API semantics (`text_link`, `text_mention`, `pre`,
`blockquote`, `expandable_blockquote`, `custom_emoji`, `date_time`, etc.).
TDLib `PreCode` becomes `pre` with `Language`; `MentionName` becomes
`text_mention` with `UserID`. UserID deliberately has `json:"-"`: a transport
must resolve/build its Bot API user object. Custom emoji IDs remain strings.
Date format flags become a canonical `DateTimeFormat` string in order
`tTdDw`, or `r`; duplicates collapse, W becomes w, R becomes r. All flag
bits (including simultaneous short/long flags) are retained. Empty means
the upstream default. `UnixTime` is limited to positive signed int32 by TDLib.

`ParseError` has Code 400, the upstream message, and source ByteOffset
when available (-1 otherwise). Reserved-character errors also provide the
known byte position, even though the upstream message omits it. Failure
returns a zero Result rather than exposing TDLib's partially mutated buffer.

## Compatibility boundaries

- This ports parsing, **not** TDLib's separate `fix_formatted_text` pass:
  it does not clean control characters, merge/remove entities, impose
  Telegram message length limits, or validate arbitrary entity nesting
  beyond the parser's own rules. No automatic bare-URL detection.
- Invalid UTF-8 is explicitly rejected with `Text must be encoded in UTF-8`
  before parsing. Upstream's low-level parser assumes valid UTF-8.
- Safe bounds checks replace upstream sentinel reads/CHECK assumptions.
- URL normalization follows the pinned C++ algorithm, not `net/url`:
  default http, lowercased ASCII host, slash insertion, special tg/ton/
  tonsite rules, control-byte escaping, canonical ports, and silent
  suppression of invalid ordinary links are preserved. IPv6 validation
  uses Go `net.ParseIP` rather than TDLib's platform IPAddress routine;
  obscure platform-specific IPv6 acceptance differences are possible.
- Date/time support reflects the pinned TDLib revision. Availability of
  its entity on a particular Bot API deployment is a transport concern.
- The upstream parser tests all pass, but this is not a claim of exhaustive
  differential equivalence for every URL/input against a compiled TDLib.

## Tests

```
go test -race ./markdownv2
go test ./markdownv2 -run '^$' -fuzz FuzzParse -fuzztime=10s -parallel=2
```

Fixtures compare exact stripped text, all entity fields/order, and exact
error message/code. Additional tests cover URL normalization, identifier
bounds, dates, error offsets, TDLib whitespace, invalid UTF-8, 20,000 seeded
random valid UTF-8 inputs, and bounded Go fuzzing with output invariants.
No tests contact Telegram or any other service.
