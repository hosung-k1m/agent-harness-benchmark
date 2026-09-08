// Package model contains the normalized, credential-free data exchanged by the benchmark.
package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
)

type Status string

const (
	StatusCompleted             Status = "completed"
	StatusTimedOut              Status = "timed_out"
	StatusFailed                Status = "failed"
	StatusInfrastructureInvalid Status = "infrastructure_invalid"
	StatusUnsupported           Status = "unsupported"
)

func (s Status) Valid() bool {
	switch s {
	case StatusCompleted, StatusTimedOut, StatusFailed, StatusInfrastructureInvalid, StatusUnsupported:
		return true
	}
	return false
}

// Classify returns the only status mapping permitted by the benchmark contract.
func Classify(agentExited, timedOut, infrastructureFailure, capabilityFailure bool) Status {
	if infrastructureFailure {
		return StatusInfrastructureInvalid
	}
	if capabilityFailure {
		return StatusUnsupported
	}
	if timedOut {
		return StatusTimedOut
	}
	if !agentExited {
		return StatusFailed
	}
	return StatusCompleted
}

type TokenQuality string

const (
	TokenProviderReported TokenQuality = "provider_reported"
	TokenClientEstimated  TokenQuality = "client_estimated"
	TokenUnavailable      TokenQuality = "unavailable"
)

func (q TokenQuality) Valid() bool {
	return q == TokenProviderReported || q == TokenClientEstimated || q == TokenUnavailable
}

// Usage uses pointers deliberately: absent provider fields serialize as JSON null, never zero.
type Usage struct {
	InputTokens           *int64       `json:"input_tokens"`
	CachedInputTokens     *int64       `json:"cached_input_tokens"`
	OutputTokens          *int64       `json:"output_tokens"`
	ReasoningOutputTokens *int64       `json:"reasoning_output_tokens"`
	TokenQuality          TokenQuality `json:"token_quality"`
}

func Int64(v int64) *int64 { return &v }
func (u Usage) Validate() error {
	if !u.TokenQuality.Valid() {
		return fmt.Errorf("invalid token quality %q", u.TokenQuality)
	}
	for _, p := range []*int64{u.InputTokens, u.CachedInputTokens, u.OutputTokens, u.ReasoningOutputTokens} {
		if p != nil && *p < 0 {
			return fmt.Errorf("token counts cannot be negative")
		}
	}
	if u.CachedInputTokens != nil && u.InputTokens != nil && *u.CachedInputTokens > *u.InputTokens {
		return fmt.Errorf("cached input exceeds input")
	}
	if u.TokenQuality == TokenUnavailable && (u.InputTokens != nil || u.CachedInputTokens != nil || u.OutputTokens != nil || u.ReasoningOutputTokens != nil) {
		return fmt.Errorf("unavailable usage must contain only null token fields")
	}
	return nil
}

type Result struct {
	SchemaVersion   string `json:"schema_version,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	CaseID          string `json:"case_id,omitempty"`
	BenchmarkID     string `json:"benchmark_id,omitempty"`
	BenchmarkTaskID string `json:"benchmark_task_id,omitempty"`
	// These retain the suite/task manifest metadata without imposing a schema
	// on third-party benchmark provenance or evaluator configuration.
	Provenance      json.RawMessage `json:"provenance,omitempty"`
	Evaluator       json.RawMessage `json:"evaluator,omitempty"`
	VariantID       string          `json:"variant_id,omitempty"`
	Trial           int             `json:"trial,omitempty"`
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	NetworkPolicyID string          `json:"network_policy_id,omitempty"`
	Status          Status          `json:"status"`
	ElapsedMillis   int64           `json:"elapsed_millis"`
	ExitCode        *int            `json:"exit_code"`
	Usage           Usage           `json:"usage"`
	VerifierPassed  *bool           `json:"verifier_passed,omitempty"`
	Verdict         string          `json:"verdict,omitempty"`
	FailureReason   string          `json:"failure_reason,omitempty"`
}

func (r Result) Validate() error {
	if !r.Status.Valid() {
		return fmt.Errorf("invalid status %q", r.Status)
	}
	if r.ElapsedMillis < 0 {
		return fmt.Errorf("negative elapsed time")
	}
	if err := r.Usage.Validate(); err != nil {
		return err
	}
	if strings.ContainsAny(r.FailureReason, "\r\n") {
		return fmt.Errorf("failure reason must be single-line")
	}
	if (r.Model == "") != (r.ReasoningEffort == "") {
		return fmt.Errorf("result model and reasoning effort must be paired")
	}
	if r.Model != "" && !manifest.ValidModelReasoning(r.Model, r.ReasoningEffort) {
		return fmt.Errorf("unexpected result model")
	}
	if r.NetworkPolicyID != "" && r.NetworkPolicyID != "unrestricted-egress-v1" {
		return fmt.Errorf("unexpected result network policy")
	}
	if (r.BenchmarkID == "") != (r.BenchmarkTaskID == "") {
		return fmt.Errorf("result benchmark id and task id must be paired")
	}
	for _, metadata := range []json.RawMessage{r.Provenance, r.Evaluator} {
		if len(metadata) != 0 && !json.Valid(metadata) {
			return fmt.Errorf("invalid benchmark metadata")
		}
	}
	if r.Verdict != "" && r.Verdict != "pass" && r.Verdict != "fail" {
		return fmt.Errorf("invalid deterministic verdict %q", r.Verdict)
	}
	if r.Verdict != "" {
		if r.VerifierPassed == nil || (r.Verdict == "pass") != *r.VerifierPassed {
			return fmt.Errorf("deterministic verdict does not match verifier result")
		}
	}
	return nil
}
