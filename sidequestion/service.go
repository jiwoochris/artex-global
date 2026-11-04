package sidequestion

import (
	"context"
	"errors"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

// Both the user-facing side-question outputs and the agent-brain prompts
// (request.go:instruction, context.go:summaryInstruction) are English, the backend source language.
var (
	errSideModelInterrupted = errors.New("The model response was interrupted. Please ask again.")
	errSideNoAnswer         = errors.New("The model returned no answer.")
)

// msgSideToolUnavailable is the answer text shown when a side question tries to
// trigger a tool call: side questions cannot run tools.
const msgSideToolUnavailable = "Tools cannot be run in a side question. Send the work request in the main conversation."

// SideQuestionService has no harness, tool executor, transcript writer or model
// failover chain. Answer is one completion; Respond adds bounded preparation
// and at most one context-overflow recovery around that completion.
type SideQuestionService struct{ Provider llm.Provider }

type Answer struct {
	Text    string
	Usage   llm.Usage
	ToolUse bool
}

func (s SideQuestionService) Answer(ctx context.Context, req llm.CompletionRequest, streaming bool, update func(Answer)) (out Answer, err error) {
	if streaming {
		complete := false
		for ev, streamErr := range s.Provider.Stream(ctx, req) {
			if streamErr != nil {
				err = streamErr
				break
			}
			switch ev.Type {
			case llm.SETextDelta:
				out.Text += ev.Text
			case llm.SEToolUseStart:
				out.ToolUse = true
			case llm.SEMessageStart, llm.SEMessageDelta:
				out.Usage.Add(ev.Usage)
			case llm.SEMessageStop:
				complete = true
			}
			if update != nil {
				update(out)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
		}
		if err == nil && !complete {
			err = errSideModelInterrupted
		}
	} else {
		var msg llm.Message
		msg, _, out.Usage, err = s.Provider.Complete(ctx, req)
		out.Text, out.ToolUse = msg.Text(), len(msg.ToolUses()) > 0
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && strings.TrimSpace(out.Text) == "" {
		if out.ToolUse {
			out.Text = msgSideToolUnavailable
		} else {
			err = errSideNoAnswer
		}
	}
	return out, err
}
