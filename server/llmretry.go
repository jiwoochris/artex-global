package server

import (
	"time"

	"github.com/yukiteruamano/artex/agent"
	"github.com/yukiteruamano/artex/db"
)

// Server-side resolution of the retry policy, see the retry section of
// docs/architecture.md. Of the five layers:
//   - connect / empty response / same-provider safety window follow the endpoint,
//     so each LLM profile may override the global default (a blank field inherits
//     the global value, and an unset global value falls back to the built-in);
//   - circuit breaker / intent replay are process-wide, so there is only one copy.
//
// The global policy is one settings row read by low-frequency call sites (building
// a provider, finishing a work item, saving config), which does not justify another
// cache layer; circuit-breaker knobs are the exception — they are read on every
// failure path, so applyRetryPolicy pushes them into the Registry.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Attempt counts keep their raw meaning here: 0 = default, negative = off. The
		// SDK's MaxRetries / EmptyResponseRetries are isomorphic, so let it interpret them.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Default circuit breaker (polling cooldown) values match the ones built into
// llmpool — they are only overridden when the user configured a value. Intent-replay
// defaults live in engine.go as modelErrorRetries / modelErrorRetryBackoff.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob, "empty
// response retries": the two are two means to the same end. The SDK layer covers "not
// a single content block", by resending the same request verbatim; this layer covers
// "thinking only, no text and no tool call", by appending an instruction that makes
// the model continue from the thinking it already has (resending verbatim is useless
// against idling that the shape of the context itself causes). The emptiness test
// differs because the SDK counts yielded events and a thinking delta is itself an
// event — but a user setting "how many times to retry an empty response" means "try
// again when the model produced nothing of substance", so both layers sharing one
// count matches that mental model.
//
// It reads the global policy rather than a profile override: a run can fail over to
// another profile midway, and this is the total budget for the whole intent, so it
// must not change with the endpoint. Semantics match the SDK's emptyRetries():
// 0 = default emptyTurnNudges; negative = no idle continuation; >0 = use that value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
