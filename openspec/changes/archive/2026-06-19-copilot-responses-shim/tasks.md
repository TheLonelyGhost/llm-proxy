## 1. Request translator

- [ ] 1.1 New `internal/providers/copilot/responses_shim.go` with
  `translateRequest(raw []byte) (chatBody []byte, requestedModel string, stream bool, err error)`.
- [ ] 1.2 Map `input` (string OR array of input items) → `messages`. For each
  array item, flatten `content` parts: single `input_text` → string content;
  multiple/mixed parts → chat content-parts array.
- [ ] 1.3 Map `tools: [{type, name, description, parameters}]` →
  `tools: [{type:"function", function:{name, description, parameters}}]`.
  Forward `tool_choice` verbatim.
- [ ] 1.4 Map `max_output_tokens` → `max_tokens`; pass `temperature`,
  `top_p`, `stream`, `metadata` through. Drop unknown / non-applicable
  fields (`store`, `previous_response_id`, `truncation`, `include`).
- [ ] 1.5 Set `stream_options.include_usage = true` whenever `stream: true`,
  so the final chunk carries `usage`.
- [ ] 1.6 If top-level `instructions` is present, prepend a
  `{role:"system", content:<instructions>}` message.

## 2. Non-streaming response translator

- [ ] 2.1 `translateChatCompletion(raw []byte, modelPrefix string) ([]byte, error)`.
- [ ] 2.2 Read upstream JSON, build a `response` object:
  - `id = "resp_" + upstream.id`
  - `object = "response"`
  - `created_at = upstream.created` (else `time.Now().Unix()`)
  - `status = "completed"` (or `"incomplete"` for non-stop finish reasons)
  - `model = modelPrefix`
  - `output` built from `choices[0].message`:
    - If `content` is non-empty, append `{type:"message", id:"msg_<rid>",
      status:"completed", role:"assistant", content:[{type:"output_text",
      text:<content>, annotations:[]}]}`.
    - For each `tool_calls[i]`, append `{type:"function_call",
      id:"fc_<rid>_<i>", call_id:<tool_call_id>, name, arguments:<string>,
      status:"completed"}`.
  - `usage = {input_tokens, output_tokens, total_tokens}` mapped from
    upstream `prompt_tokens / completion_tokens / total_tokens`.

## 3. Streaming response translator

- [ ] 3.1 `newResponsesSSETranslator(upstreamBody io.ReadCloser,
  modelPrefix, requestedModel string) io.ReadCloser`.
- [ ] 3.2 Scan upstream SSE on `\n\n` boundaries, parse each
  `data: <chatcmpl-chunk>` JSON.
- [ ] 3.3 On the first chunk, emit `response.created` and
  `response.in_progress` events (synthesise a stable response id from the
  upstream chunk id).
- [ ] 3.4 Track per-tool-call argument streams keyed by upstream
  `tool_call.index`. Emit `response.output_item.added` for each new
  function_call item the first time we see its index, then
  `response.function_call_arguments.delta` per fragment.
- [ ] 3.5 Track the assistant message text stream. On first text delta,
  emit `response.output_item.added` + `response.content_part.added`. Then
  emit `response.output_text.delta` per chunk.
- [ ] 3.6 On `finish_reason` or `[DONE]`: emit the matching `*.done` events
  in order — content-part, output-item, then `response.completed` carrying
  the translated `usage`. Then close the stream.
- [ ] 3.7 SSE comments (`:keep-alive`) pass through verbatim. Malformed
  upstream JSON is dropped with a DEBUG log; we never crash the stream.

## 4. Wire into Copilot provider

- [ ] 4.1 Replace the stub at `internal/providers/copilot/copilot.go:275`
  with: `allowed(model)`; translate request; call
  `p.forward(ctx, "POST", "/chat/completions", chatBody, stream)`; on the
  returned `*backend.Response`, dispatch by content-type and `stream` flag
  to the JSON translator or the SSE translator; return a new
  `*backend.Response` whose `Body` is the translated stream.
- [ ] 4.2 Preserve the original 401-retry behaviour by relying on `forward`
  (no duplication).
- [ ] 4.3 On upstream non-2xx, propagate the upstream response unchanged so
  the server's existing error handling produces the right envelope.

## 5. Transform-layer pass-through

- [ ] 5.1 In `internal/transform/transform.go::TransformResponsesResponse`
  non-streaming branch, after parsing the raw body, check for top-level
  `output` (array). If present, rewrite `model` to `modelPrefix` and
  return the body re-encoded with `Content-Type: application/json` and
  `StatusCode: 200`. Otherwise fall through to the existing `choices` path.

## 6. Tests

- [ ] 6.1 `responses_shim_test.go`: request translator table — string
  input, array input with single `input_text`, mixed content parts, tools
  reshape, `max_output_tokens` → `max_tokens`, `instructions` prepends
  system message, `stream_options.include_usage` is set on stream.
- [ ] 6.2 Non-streaming translator: chat completion with text only;
  chat completion with text + two `tool_calls`; chat completion with
  `finish_reason=length` → `status:"incomplete"`.
- [ ] 6.3 Streaming translator: feed a representative chat-chunk SSE
  sequence (text only) and assert the exact ordered event sequence; same
  for a tool-call sequence; verify final `usage` lands in
  `response.completed`.
- [ ] 6.4 End-to-end with `httptest.Server`: Copilot upstream replies with
  a canned chat response; verify the proxy's `provider.Responses` returns
  Responses-shaped JSON (non-streaming) and named events (streaming).
- [ ] 6.5 Allow-list and 401-refresh tests already exist for Chat; add a
  thin parallel test for Responses to confirm both still apply.
- [ ] 6.6 Transform layer: pass-through case (body with `output`) returns
  the body verbatim aside from `model`; legacy case (body with `choices`)
  still maps as before.

## 7. Docs

- [ ] 7.1 `docs/providers/github-copilot.md` — new section "Responses API
  shim": supported subset (text input, function tools, streaming),
  unsupported (`previous_response_id`, built-in tools, retrieval), and how
  IDs are synthesised.
- [ ] 7.2 `docs/configuration.md` — note that `copilot/<model>` now
  supports `/v1/responses`; bob continues to return 501.

## 8. Verification

- [ ] 8.1 `task fmt && task vet && task lint && task nilaway && task vuln`.
- [ ] 8.2 `task test:cover` — keep coverage ≥ 80%.
- [ ] 8.3 Live smoke test against the running proxy with a real OpenAI
  Responses-style request (curl + sample payload) hitting
  `copilot/<allow-listed-model>` and verify named events arrive over SSE.
- [ ] 8.4 `task check` end-to-end before declaring done.