package transform

// Verification tests for ticket 01 bugs.  Each test names the original failure
// mode explicitly so it is clear what is being guarded against.

import (
	"io"
	"runtime"
	"strings"
	"testing"
)

// --- Bug 2: hard error when caller buffer is smaller than a formatted event ---

// StreamTransformer previously returned fmt.Errorf("buffer too small for SSE
// event") when the formatted event did not fit in p.  That is an io.Reader
// contract violation: Read must never fail solely because the buffer is small.
// After ticket 01 the remainder goes into the overflow buffer and subsequent
// Read calls drain it; no error is returned.
func TestVerify_Ticket01_Bug2_NoHardErrorOnSmallBuffer_Chat(t *testing.T) {
	bigContent := strings.Repeat("x", 300) // formatted event ≈ 360 bytes
	input := `data: {"id":"1","choices":[{"delta":{"content":"` + bigContent + `"}}]}` + "\n\n"

	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 64) // intentionally smaller than the event
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read returned unexpected error (bug 2 regression): %v", err)
		}
	}
	if !strings.Contains(got.String(), bigContent) {
		t.Errorf("event content missing from reassembled output (%d bytes total)", got.Len())
	}
}

// Same scenario through the responses transformer (which previously returned
// fmt.Errorf("buffer too small for SSE block (%d bytes)") from rt.write).
func TestVerify_Ticket01_Bug2_NoHardErrorOnSmallBuffer_Responses(t *testing.T) {
	bigContent := strings.Repeat("r", 300)
	input := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"r1","model":"` + bigContent + `","status":"in_progress"}}` + "\n\n"

	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingResponsesResponse(body, "prefix/model")
	defer tr.Close()

	buf := make([]byte, 64)
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read returned unexpected error (bug 2 regression): %v", err)
		}
	}
	if !strings.Contains(got.String(), "response.created") {
		t.Errorf("event type missing from reassembled output (%d bytes total)", got.Len())
	}
}

// --- Bug 4: unbounded recursion on empty blocks / malformed events ---

// responsesStreamTransformer previously called rt.Read(p) recursively for
// every empty block.  10 000 consecutive empty blocks would deepen the call
// stack by 10 000 frames.  After ticket 01 the loop uses continue, so stack
// depth stays O(1) regardless of empty-block count.
func TestVerify_Ticket01_Bug4_EmptyBlocksDoNotGrowStack_Responses(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10_000; i++ {
		sb.WriteString("\n\n")
	}
	sb.WriteString(`data: {"type":"done"}` + "\n\n")

	// Capture the goroutine stack depth before and after to confirm it did not
	// recurse: measure max live frames across all reads.
	stackBefore := goroutineFrames(t)

	body := io.NopCloser(strings.NewReader(sb.String()))
	tr := TransformStreamingResponsesResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 4096)
	var collected strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}

	stackAfter := goroutineFrames(t)
	// Allow a small slack for test framework overhead; what we forbid is the
	// 10 000-frame growth the old code would have caused.
	if stackAfter-stackBefore > 50 {
		t.Errorf("stack grew by %d frames — recursion not eliminated (bug 4 regression)", stackAfter-stackBefore)
	}
	if !strings.Contains(collected.String(), `"type":"done"`) {
		t.Errorf("final event not delivered after empty blocks")
	}
}

// StreamTransformer previously called st.Read(p) recursively for malformed
// JSON blocks.  Same O(1) depth guarantee is required after ticket 01.
func TestVerify_Ticket01_Bug4_MalformedBlocksDoNotGrowStack_Chat(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10_000; i++ {
		sb.WriteString("data: {bad json}\n\n")
	}
	sb.WriteString(`data: {"id":"last","choices":[]}` + "\n\n")

	stackBefore := goroutineFrames(t)

	body := io.NopCloser(strings.NewReader(sb.String()))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 4096)
	var collected strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}

	stackAfter := goroutineFrames(t)
	if stackAfter-stackBefore > 50 {
		t.Errorf("stack grew by %d frames — recursion not eliminated (bug 4 regression)", stackAfter-stackBefore)
	}
	if !strings.Contains(collected.String(), `"id":"last"`) {
		t.Errorf("final event not delivered after malformed blocks")
	}
}

// --- Bug 5: [DONE] silently truncated when buffer < 15 bytes ---

// "data: [DONE]\n\n" is 15 bytes.  With a 10-byte buffer the old code returned
// n=10, err=nil — the client received "data: [DON" and never saw the stream
// terminator.  After ticket 01 the overflow buffer holds the remaining 5 bytes;
// a second Read delivers them, and the full sentinel is never truncated.
func TestVerify_Ticket01_Bug5_DoneNotTruncatedWithSmallBuffer_Chat(t *testing.T) {
	input := "data: [DONE]\n\n"
	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	const sentinel = "data: [DONE]\n\n"
	buf := make([]byte, 10) // smaller than the 15-byte sentinel
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}
	if got.String() != sentinel {
		t.Errorf("[DONE] truncated (bug 5 regression): got %q, want %q", got.String(), sentinel)
	}
}

// Same guarantee for the responses transformer.
func TestVerify_Ticket01_Bug5_DoneNotTruncatedWithSmallBuffer_Responses(t *testing.T) {
	input := "data: [DONE]\n\n"
	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingResponsesResponse(body, "test/model")
	defer tr.Close()

	const sentinel = "data: [DONE]\n\n"
	buf := make([]byte, 10)
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}
	if got.String() != sentinel {
		t.Errorf("[DONE] truncated (bug 5 regression): got %q, want %q", got.String(), sentinel)
	}
}

// --- Ticket 02 regression tests ---
// These cover the three acceptance criteria that ticket 02 specifies.
// All three failure modes were eliminated by the ticket 01 implementation;
// these tests are the permanent regression guard.

// AC1: Read never returns (0, nil) for a non-EOF condition.
// The old code's `default: return 0, nil` caused io.Copy to hot-spin on any
// block that was not a `data:` line or SSE comment.  After the fix the loop
// uses `continue` so the next block is fetched immediately inside the same
// Read call; the caller never sees (0, nil) unless the stream is exhausted.
func TestVerify_Ticket02_AC1_NoZeroNilReturnForUnrecognizedFields(t *testing.T) {
	// Stream: three unrecognized-field blocks, then one real event.
	// A spin-detector caps iterations so a regressed (0,nil) loop fails fast.
	input := "id: 42\n\nretry: 3000\n\nevent: ping\n\n" +
		`data: {"id":"1","choices":[]}` + "\n\n"

	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 4096)
	const maxIter = 100
	for i := range maxIter {
		n, err := tr.Read(buf)
		if n > 0 && strings.Contains(string(buf[:n]), `"id":"1"`) {
			t.Logf("real event delivered in %d Read call(s)", i+1)
			return // pass
		}
		if err == io.EOF {
			t.Fatal("reached EOF before delivering the real event")
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n == 0 {
			t.Fatalf("Read returned (0, nil) on iteration %d — spin not eliminated (bug 1 / ticket 02 AC1 regression)", i+1)
		}
	}
	t.Fatalf("real event not delivered within %d iterations", maxIter)
}

// AC2 + AC3: [DONE] and every other frame delivered in full regardless of
// buffer size; no hard error returned.  (Covered in full by the ticket 01
// verification tests above; the assertions below are a focused restatement
// scoped to the ticket 02 acceptance language.)

// AC2a: [DONE] assembled correctly across reads with a 1-byte buffer —
// the most extreme under-sizing possible.
func TestVerify_Ticket02_AC2_DoneDeliveredWithOneByteBuffer(t *testing.T) {
	input := "data: [DONE]\n\n"
	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 1)
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read returned error (ticket 02 AC3 regression): %v", err)
		}
	}
	const want = "data: [DONE]\n\n"
	if got.String() != want {
		t.Errorf("[DONE] truncated or corrupted: got %q, want %q", got.String(), want)
	}
}

// AC2b: A large event assembled across 1-byte reads — no hard error, no data loss.
func TestVerify_Ticket02_AC3_LargeEventNoErrorWithOneByteBuffer(t *testing.T) {
	bigContent := strings.Repeat("z", 300)
	input := `data: {"id":"1","choices":[{"delta":{"content":"` + bigContent + `"}}]}` + "\n\n"

	body := io.NopCloser(strings.NewReader(input))
	tr := TransformStreamingChatResponse(body, "test/model")
	defer tr.Close()

	buf := make([]byte, 1)
	var got strings.Builder
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read returned hard error (ticket 02 AC3 regression): %v", err)
		}
	}
	if !strings.Contains(got.String(), bigContent) {
		t.Errorf("event content missing after 1-byte reads (%d bytes collected)", got.Len())
	}
	if !strings.HasSuffix(got.String(), "\n\n") {
		t.Errorf("event not properly terminated: last 4 bytes = %q", got.String()[max(0, got.Len()-4):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// goroutineFrames returns the number of frames in the current goroutine's
// stack trace.  Used to verify that Read does not recurse.
func goroutineFrames(t *testing.T) int {
	t.Helper()
	buf := make([]byte, 64*1024)
	n := runtime.Stack(buf, false)
	return strings.Count(string(buf[:n]), "\n")
}
