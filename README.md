# tgui

Telegram bot transport and presentation primitives for Go. Standard library only;
no model SDK, agent framework, database or background workers.

```sh
go get github.com/zlobober/tgui@v0.1.0
```

## Features

- Telegram polling, messages, edits, documents, buttons and command registration.
- Animated draft previews with conservative edit fallback and throttled updates.
- Strict, offline Telegram MarkdownV2 parsing into native message entities.
- Formatted streaming previews from cumulative Markdown source.
- UTF-16-aware splitting, preserved entities and complete final delivery.
- Redacted transport errors, partial-delivery references and no blind write retries.

Applications own authorization, polling offsets, persistence, scheduling, model
calls, confirmations, cancellation and retry policy. This library is not a bot
framework. Its API is experimental (v0.x).

## Basic use

```go
import (
    "context"
    "os"

    "github.com/zlobober/tgui"
)

func send(ctx context.Context, chat tgui.ChatID) error {
    client, err := tgui.NewClient(os.Getenv("TELEGRAM_BOT_TOKEN"))
    if err != nil { return err }
    text, err := tgui.Markdown("*Hello* from [Go](https://go.dev)\\!")
    if err != nil { return err }
    _, err = client.Send(ctx, chat, tgui.Message{Text: text})
    return err
}
```

`Send` returns successful message references even when a later chunk fails. Buttons
are attached to the final chunk. `Edit`, `Draft`, `SendDocument`, `GetUpdates`,
`SetCommands`, `SetCommandsForChatMember`, `AnswerCallback`, `Typing` and `Me` provide the underlying operations.

Call command registration explicitly at application startup so Telegram advertises
slash commands. `SetCommands(ctx, commands)` sets the default menu;
`SetCommandsForChatMember(ctx, chatID, userID, commands)` scopes it to one user
in a group/supergroup. Menus do not install handlers or grant authorization.
Command names contain lowercase letters, digits and underscores (no leading
slash or hyphens). Empty lists clear that scope. Telegram clients may cache menus.
For forum-topic sends, set `Message.ThreadID`; it is preserved on every split chunk.
`Message.DisableLinkPreview` disables link previews on sends/edits. Incoming messages
expose `MessageThreadID`, `Date`, and `SenderChat` for application-owned authorization.
For streaming forum responses, set `ResponseOptions.ThreadID` (and optionally
`DisableLinkPreview`). Every preview and final chunk stays in that topic. Topic
responses use edit previews, including when `Mode` is Auto or Draft, because the
transport's Draft method is unscoped. Finalization edits the preview into the first
final chunk and sends only remaining chunks; it does not duplicate the preview.

`Call(ctx, method, payload, result)` is a low-level escape hatch for methods without
named helpers, such as `createForumTopic`, `editForumTopic`, and `getChatMember`.
It keeps the same redacted, bounded transport and no-retry contract; applications
must validate payloads and results. Method names are restricted to ASCII letters,
and methods not explicitly recognized as read-only are treated as uncertain writes
on transport failure.

Use `WithHTTPClient` and `WithBaseURL` for configuration/testing. HTTP is accepted
only on loopback; otherwise HTTPS is required. Redirects are disabled.

## Streaming previews

```go
response := client.NewResponse(chat, tgui.ResponseOptions{
    Mode: tgui.PreviewAuto,
    MinInterval: 1500 * time.Millisecond,
})
response.Update(tgui.Plain("Working…"))
_ = response.Flush(ctx)

// Feed the full accumulated source, not an individual delta.
response.UpdateMarkdown(accumulatedMarkdown)
_ = response.Flush(ctx) // call from your ticker or event loop

final, err := tgui.Markdown(completeAnswer)
if err != nil { final = tgui.Plain(completeAnswer) }
refs, err := response.Finish(ctx, tgui.Message{Text: final})
```

- Modes: `PreviewAuto`, `PreviewDraft`, `PreviewEdit`, `PreviewNone`.
- Auto prefers `sendMessageDraft`. Only definitive unsupported-method/capability
  errors trigger placeholder-message fallback, not generic failures or rate limits.
- `Update` retains plain-text progress behavior. `UpdateMarkdown` enables Telegram MarkdownV2 formatting.
- `UpdateFormatted(Text)` accepts validated native entities from an application-owned renderer (such as CommonMark), copies them, and preserves them through bounded preview cropping. It performs no network I/O. Invalid entity offsets/nesting return an error without replacing the previous preview.
- On a throttled `Flush`, the library tries the complete Markdown source, then
  removes trailing Unicode characters until it finds the longest parseable prefix.
  Search is bounded to 512 candidates and 64 KiB source. An unmatched final
  backslash is held back. If no usable prefix is found, the last preview remains.
- Every update is cumulative: incomplete markup automatically returns in the next
  update, without a separate suffix buffer. Final validation remains independent.
- Preview tails are bounded in rendered UTF-16 units and marked with an ellipsis.
  Entities are clipped/rebased; custom emoji and date entities stay atomic.
- Final output is never silently truncated. Sending the permanent message retires
  the draft. **Do not send whitespace or empty drafts afterward:** whitespace can
  create a blank bubble, while empty text requests a Thinking placeholder.
- Successful `Finish` is idempotent. Partial/ambiguous delivery becomes terminal
  (`ErrResponseUncertain`) so repeated finalization cannot duplicate output.
- Methods are concurrency-safe; network operations serialize per Response. No
  timers or goroutines are started by the library. The caller owns scheduling.
- For interruption notices, call `Finish` with a fresh bounded context. This cannot
  undo external actions. If final delivery fails, an ephemeral draft expires naturally.

## Formatting

This is **Telegram MarkdownV2**, not CommonMark: `*bold*`, `_italic_`,
`__underline__`, `~strike~`, `||spoiler||`, inline/fenced code, links and quotes.
HTML, tables and CommonMark document rendering are not implemented.

`FormattingInstructions()` supplies concise model-facing guidance. `Validate`
returns diagnostics, and `Markdown` returns stripped text with UTF-16 entities.
Transport uses explicit entities, not `parse_mode`, avoiding a second Markdown parse.
For application-generated text, use `Plain`, `Bold`, `Italic`, `Code`, `Pre`, `Link`
and `Join` instead of building markup manually.

The [parser](markdownv2/README.md) is a pure-Go port of TDLib's `parse_markdown_v2`
at pinned revision `42e6a5259551178d1dab54a22ad96d14bd906e20`. All 121 active literal
upstream cases plus the upstream ASCII loop are included with attribution and license.
No TDLib/C++ runtime is needed. The parser README documents compatibility boundaries.

`markdownv2.Parse` preserves low-level TDLib semantics; `tgui.Markdown` and
`Validate` additionally check transport constraints such as entity nesting and
supported URL payloads. Successful parsing does not imply every Telegram deployment
supports every entity type. `Split` rejects invalid UTF-8/entity boundaries and
chunk limits too small for an atomic entity rather than corrupting text.

Model repair is deliberately application-owned: validate, optionally make one
formatting-only call without tools, validate again, and otherwise use literal
plaintext. Never rerun an entire agent or external action just to repair formatting.
Syntax validation does not prove that a model preserved the original meaning.

## Delivery and security

`APIError` contains a method, code, RetryAfter and capability/uncertainty flags,
not raw server messages, payloads or token-bearing URLs. Do not install an HTTP
transport that logs credentials. Text, entity, response and document sizes are bounded.

The client does not retry writes. Returned partial references identify successfully
sent chunks. `DeliveryUncertain` means a failed write may have reached Telegram;
never resend the whole message blindly. A definite 429 rejection exposes RetryAfter
for caller scheduling; `Response` also throttles future previews. Repeated edits
reporting NotModified are treated as successful no-ops during response finalization.

Do not run competing pollers. Claim updates durably and authorize the sender in the
application before invoking sensitive tools or delivering private data.

## Tests

```sh
go test -race -count=1 -timeout 60s ./...
go vet ./...
go test ./markdownv2 -run '^$' -fuzz FuzzParse -fuzztime=10s -parallel=2
```

Tests are offline and use synthetic fixtures/HTTP mocks. They cover parsing,
Unicode/entities, formatted prefixes, transport errors, partial delivery, fallback,
rate limits and response lifecycle concurrency.

## License

Original library code is licensed under Apache-2.0; see [LICENSE](LICENSE).
The TDLib-derived parser and upstream test corpus remain under the Boost Software
License 1.0, with their copyright and license notices preserved; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and
[markdownv2/LICENSE_1_0.txt](markdownv2/LICENSE_1_0.txt).
