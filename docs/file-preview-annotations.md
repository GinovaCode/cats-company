# Ordinary file preview annotations

File previews use the file's original conversation and persisted attachment reference. They do not need a Gateway application or an application registry entry. Gateway application annotations continue to use their existing contract.

The chat image/video actions and the conversation file list open the same file preview panel. Chat payloads use the persisted-attachment filename fallback and historical name/size/MIME aliases before comparison with the server descriptor. This allows nameless chat images to match the same canonical file-list entry without relaxing source validation. In annotation mode:

- Images use regions normalized to the actual image pixels, so scaling and letterboxing do not change the anchor.
- Videos pause on the selected frame and save its time in seconds with a normalized region.
- PDFs use the built-in canvas reader on desktop and mobile, and save the page number plus a normalized page region.
- Text and code use UTF-16 character offsets, the selected quotation, and line numbers. Markdown and HTML show their original source in text annotation mode.
- CSV and Excel previews use the sheet name and a range of one-based rows and columns. Anchors are limited to the preview's supported rows/columns.

Comments can be added, edited, and removed before sending. Every supported file type captures its own screenshot pair by default. Text/code (including Markdown and HTML source) and CSV/Excel cells capture the **currently visible preview** plus the selected region and nearby content; they retain their existing UTF-16 or cell-range anchors. This is not a screenshot of an entire long file or workbook. Images, PDF pages, and paused video frames continue to use their actual media pixels.

Each comment owns its own screenshot pair; comments from different PDF pages, video times, text selections or cell ranges must not reuse a screenshot. Media pixels are copied at selection time, before the source-binding network request completes, and accepted only after source validation. Entering whole-page PDF mode waits for the first complete canvas render. Clipped rectangle calculations stay inside the strict normalized-coordinate contract even when floating-point division rounds at page edges.

A failed submit retains the unsent comments for retry. Captured geometry and images become stale when the underlying preview changes, including text edits, sheet switches, scroll, resize, PDF page/zoom and video seek changes. The comment popover distinguishes source validation failures from capture failures and lets the user retry the same selection while keeping their comment. An explicit text-only option is available for every file type; opting out sends the quotation/location without screenshot evidence.

## Source and version

The parent UI supplies only the original `topic_id`, `message_id`, and filtered `attachment_index` to open a binding. The server looks up that exact message and attachment, checks read/publish permissions, and returns its canonical source descriptor. Attachment indices follow `fileAttachmentsFromMessage`, excluding text and tool blocks; they are not raw content-block indices.

`source.version` is a SHA-256 digest of the persisted attachment descriptor, including the source message and attachment identity. It identifies the stored attachment reference and is **not** a digest of arbitrary remote file bytes. Original upload keys should continue to refer to immutable uploaded files.

## HTTP contract

All three endpoints require the user's normal authenticated session:

- `POST /api/files/open-bindings` with `{topic_id, message_id, attachment_index}` returns a `catsco.file-open-binding.v1` object with `open_ref`, `topic_id`, `expires_at`, and `source`.
- `POST /api/files/annotations` takes `{open_ref, client_msg_id, file_annotations, content_blocks?}`. `file_annotations` has contract `catsco.file-annotations.v1`, the exact canonical `source`, and bounded `annotations`.
- `DELETE /api/files/open-bindings/{open_ref}` releases the open instance.

The opaque reference is held only by the parent UI. It is bound to the actor and actual auth session, expires with the session or the binding TTL, and is revoked when the editor closes. It is not persisted in message metadata. Restarting the service or using a different replica invalidates process-local references; reopening the file creates a new reference.

The submit endpoint derives its destination and user-visible content on the server. It rechecks access and the source version, validates optional owned JPEG uploads, then uses normal message persistence, idempotency, history, and recipient delivery. Ordinary message endpoints cannot turn client-provided `file_annotations` into authoritative bound annotations.

## Target shapes

Each annotation has `id`, `kind`, `body`, and `target`:

| Kind | Target |
| --- | --- |
| `image` | `{rect: {x, y, width, height}}` |
| `video` | `{rect, time_seconds}` |
| `pdf` | `{rect, page}` |
| `text` | `{start, end, quote, line_start, line_end}` |
| `cells` | `{sheet, row_start, row_end, column_start, column_end, quote}` |

Rectangles are finite normalized values in the actual media/page bounds. Text offsets are UTF-16 code units, matching browser string and DOM selection semantics, including Chinese text and emoji.

Screenshot evidence consists of two JPEG images, `full` and `crop`, derived from one captured bitmap, with a visible red selection border. Each is at most 2048 pixels per dimension and 2 MiB. DOM previews lazily load the existing self-hosted html2canvas-pro 1.6.7 resource at `/catsco-runtime/html2canvas-pro-1.6.7.min.js`, verify SRI, preserve any existing renderer global, and enforce a capture timeout. Modern CSS colors are supported without a CDN or Gateway deployment. Upload receipts establish ownership and verify file bytes and dimensions before message attachment. No screen-sharing permission or arbitrary server-side URL fetch is used.
