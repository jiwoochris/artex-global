package agent

import (
	"errors"
	"testing"

	"github.com/Autumn-27/norma/harness"
)

// isUnproductiveNoop gates the thinking-only no-op rescue and the exhausted
// classification for issue #70: a run that ended naturally but never acted.
func TestIsUnproductiveNoop(t *testing.T) {
	cases := []struct {
		name      string
		reason    harness.TerminalReason
		err       error
		toolCalls int
		finalText string
		want      bool
	}{
		{"thinking-only no-op", harness.ReasonCompleted, nil, 0, "", true},
		{"blank final text still a no-op", harness.ReasonCompleted, nil, 0, "  \n ", true},
		{"called a tool is productive", harness.ReasonCompleted, nil, 1, "", false},
		{"left a conclusion is productive", harness.ReasonCompleted, nil, 0, "no open ports found", false},
		{"max_turns is not a no-op", harness.ReasonMaxTurns, nil, 0, "", false},
		{"timeout is not a no-op", harness.ReasonTimeout, nil, 0, "", false},
		{"model_error is not a no-op", harness.ReasonModelError, nil, 0, "", false},
		{"an error is not a no-op", harness.ReasonCompleted, errors.New("boom"), 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUnproductiveNoop(c.reason, c.err, c.toolCalls, c.finalText); got != c.want {
				t.Fatalf("isUnproductiveNoop(%q, %v, %d, %q) = %v, want %v",
					c.reason, c.err, c.toolCalls, c.finalText, got, c.want)
			}
		})
	}
}
