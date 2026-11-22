// Package cost — расход этапов прогона: итог и последние лимиты подписки драйверов.
package cost

import "github.com/hakastein/smith/agent"

// Stage — этап прогона в отчёте о расходе: исход драйвера под именем этапа и вердикт того, кто этап запускал.
type Stage struct {
	Name, Driver string
	// OK и Reason — вердикт этапа: исход драйвера, если запустивший не сорвал этап по своим правилам.
	OK      bool
	Reason  string
	Outcome *agent.Outcome
}

func peakContext(stages []*Stage) int64 {
	var peak int64
	for _, s := range stages {
		peak = max(peak, s.Outcome.ContextTokens)
	}
	return peak
}

func usd(stages []*Stage) float64 {
	sum := 0.0
	for _, s := range stages {
		if s.Outcome.Cost != nil {
			sum += *s.Outcome.Cost
		}
	}
	return sum
}

// Summary — итог расхода этапов.
type Summary struct {
	USD           float64 `json:"usd" yaml:"usd"`
	USDKnown      bool    `json:"usdKnown" yaml:"usdKnown"`
	Turns         int     `json:"turns" yaml:"turns"`
	Tokens        int64   `json:"tokens" yaml:"tokens"`
	ContextTokens int64   `json:"contextTokens" yaml:"contextTokens"`
	DurationMs    int64   `json:"durationMs" yaml:"durationMs"`
}

// Summarize — итог расхода этапов.
func Summarize(stages []*Stage) Summary {
	c := Summary{USD: usd(stages), USDKnown: true, ContextTokens: peakContext(stages)}
	for _, s := range stages {
		if s.Outcome.Cost == nil || s.Outcome.UsageApprox {
			c.USDKnown = false
		}
		c.Turns += s.Outcome.Turns
		c.Tokens += s.Outcome.Tokens()
		c.DurationMs += s.Outcome.DurationMs
	}
	return c
}

// DriverLimits — последние сведения о лимитах каждого драйвера за прогон;
// драйвер, не приславший ни окна, ни отказа, не попадает.
// shortcut: «последние» — по порядку этапов, а не по времени события: у
// параллельных этапов одной волны расходятся на минуты — брать время события, если это станет важно.
func DriverLimits(stages []*Stage) map[string]*agent.Limits {
	out := map[string]*agent.Limits{}
	for _, s := range stages {
		if l := s.Outcome.Limits; l != nil && (len(l.Windows) > 0 || (l.Status != nil && *l.Status != "" && *l.Status != "allowed")) {
			out[s.Driver] = l
		}
	}
	return out
}
