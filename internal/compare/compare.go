package compare

import (
	"fmt"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"math/rand"
	"sort"
	"strings"
)

func Median(values []*int64) *int64 {
	var v []int64
	for _, x := range values {
		if x != nil {
			v = append(v, *x)
		}
	}
	if len(v) == 0 {
		return nil
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	n := len(v)
	if n%2 == 1 {
		return model.Int64(v[n/2])
	}
	return model.Int64((v[n/2-1] + v[n/2]) / 2)
}

// Order returns a reproducible shuffled copy; callers persist seed and returned order.
func Order(variants []string, seed int64) []string {
	o := append([]string(nil), variants...)
	rand.New(rand.NewSource(seed)).Shuffle(len(o), func(i, j int) { o[i], o[j] = o[j], o[i] })
	return o
}

type Summary struct {
	Label   string
	Results []model.Result
}

func (s Summary) Passes() int {
	n := 0
	for _, r := range s.Results {
		if r.Status == model.StatusCompleted && r.VerifierPassed != nil && *r.VerifierPassed {
			n++
		}
	}
	return n
}
func (s Summary) elapsed() *int64 {
	v := make([]*int64, len(s.Results))
	for i, r := range s.Results {
		v[i] = model.Int64(r.ElapsedMillis)
	}
	return Median(v)
}
func token(s Summary, pick func(model.Usage) *int64) *int64 {
	var v []*int64
	for _, r := range s.Results {
		if r.Usage.TokenQuality == model.TokenProviderReported {
			v = append(v, pick(r.Usage))
		}
	}
	return Median(v)
}
func display(p *int64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *p)
}
func quality(s Summary) string {
	for _, r := range s.Results {
		if r.Usage.TokenQuality == model.TokenProviderReported {
			return string(model.TokenProviderReported)
		}
	}
	return string(model.TokenUnavailable)
}
func RenderMarkdown(trials int, summaries []Summary) string {
	var b strings.Builder
	b.WriteString("| Harness | Passes / " + fmt.Sprint(trials) + " | Median elapsed | Median input | Median cached input | Median output | Token quality |\n|---|---:|---:|---:|---:|---:|---|\n")
	for _, s := range summaries {
		b.WriteString(fmt.Sprintf("| %s | %d / %d | %sms | %s | %s | %s | %s |\n", s.Label, s.Passes(), trials, display(s.elapsed()), display(token(s, func(u model.Usage) *int64 { return u.InputTokens })), display(token(s, func(u model.Usage) *int64 { return u.CachedInputTokens })), display(token(s, func(u model.Usage) *int64 { return u.OutputTokens })), quality(s)))
	}
	return b.String()
}
