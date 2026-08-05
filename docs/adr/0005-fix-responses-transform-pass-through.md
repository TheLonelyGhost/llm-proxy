# TransformResponsesResponse has an unintentional pass-through for native Responses API shapes

`TransformResponsesResponse` in `internal/transform/transform.go` contains a pass-through path: when the upstream response has a top-level `output` array (the native OpenAI Responses API shape), it skips full re-normalisation and only rewrites the `model` field.

This is inconsistent with the proxy's intended posture (ADR-0004): the Response Transform should always fully re-normalise success responses, applying field defaulting, fix-ups, and re-encoding — the same as `TransformChatResponse` and `TransformCompletionResponse` do.

The pass-through was not intentional. The correct behaviour is full re-normalisation of the `output` array, including field defaulting and any fix-ups equivalent to those applied in the chat/completion transforms.

**This ADR records the decision; the fix is a separate implementation task.**
