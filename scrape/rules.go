// Copyright 2022 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scrape

import (
	"context"
	"time"

	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
)

type RuleEngine interface {
	NewScrapeBatch() Batch
	EvaluateRules(b Batch, ts time.Time) ([]*Sample, error)
}

// ruleEngine evaluates rules from individual targets at scrape time.
type ruleEngine struct {
	targetLabels labels.Labels
	rules        []*config.ScrapeRuleConfig
	engine       *promql.Engine
}

// newRuleEngine creates a new RuleEngine.
func newRuleEngine(targetLabels labels.Labels, rules []*config.ScrapeRuleConfig) RuleEngine {
	if len(rules) == 0 {
		return &nopRuleEngine{}
	}

	return &ruleEngine{
		targetLabels: targetLabels,
		rules:        rules,
		engine: promql.NewEngine(promql.EngineOpts{
			MaxSamples:    50000000,
			Timeout:       10 * time.Second,
			LookbackDelta: 5 * time.Minute,
		}),
	}
}

// NewScrapeBatch creates a new Batch which can be used to add samples from a single scrape.
// Rules are always evaluated on a single Batch.
func (r *ruleEngine) NewScrapeBatch() Batch {
	return &batch{
		samples: make([]*Sample, 0),
	}
}

// EvaluateRules executes rules on the given Batch and returns new Samples.
func (r *ruleEngine) EvaluateRules(b Batch, ts time.Time) ([]*Sample, error) {
	var result []*Sample
	for _, rule := range r.rules {
		queryable := storage.QueryableFunc(func(ctx context.Context, mint, maxt int64) (storage.Querier, error) {
			return b, nil
		})

		query, err := r.engine.NewInstantQuery(queryable, nil, rule.Expr, ts)
		if err != nil {
			return nil, err
		}

		samples, err := query.Exec(context.Background()).Vector()
		if err != nil {
			return nil, err
		}

		for _, s := range samples {
			lbls := s.Metric.WithoutLabels("__name__")
			lbls = append(lbls, labels.Label{
				Name:  "__name__",
				Value: rule.Record,
			})
			lbls = append(lbls, r.targetLabels...)

			result = append(result, &Sample{
				metric: lbls,
				t:      s.T,
				v:      s.V,
			})
		}
	}

	return result, nil
}

// Batch is used to collect samples from a single scrape.
type Batch interface {
	storage.Querier
	Add(l labels.Labels, t int64, v float64)
}

type batch struct {
	samples []*Sample
}

type Sample struct {
	metric labels.Labels
	t      int64
	v      float64
}

func (b *batch) Add(l labels.Labels, t int64, v float64) {
	b.samples = append(b.samples, &Sample{metric: l.Copy(), t: t, v: v})
}

func (b *batch) Select(_ bool, _ *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	var samples []*Sample
	for _, s := range b.samples {
		match := true
		for _, matcher := range matchers {
			if !matcher.Matches(s.metric.Get(matcher.Name)) {
				match = false
				break
			}
		}
		if match {
			samples = append(samples, s)
		}
	}

	return &seriesSet{
		i:       -1,
		samples: samples,
	}
}

func (b *batch) LabelNames(matchers ...*labels.Matcher) ([]string, storage.Warnings, error) {
	return nil, nil, nil
}

func (b *batch) LabelValues(name string, matchers ...*labels.Matcher) ([]string, storage.Warnings, error) {
	return nil, nil, nil
}
func (b *batch) Close() error { return nil }

type seriesSet struct {
	i       int
	samples []*Sample
}

func (s *seriesSet) Next() bool {
	s.i++
	return s.i != len(s.samples)
}

func (s *seriesSet) At() storage.Series {
	sample := s.samples[s.i]
	return promql.NewStorageSeries(promql.Series{
		Metric: sample.metric,
		Points: []promql.Point{
			{
				T: sample.t,
				V: sample.v,
			},
		},
	})
}

func (s *seriesSet) Err() error                 { return nil }
func (s *seriesSet) Warnings() storage.Warnings { return nil }

// nopRuleEngine does not produce any new samples when evaluating rules.
type nopRuleEngine struct{}

func (n nopRuleEngine) NewScrapeBatch() Batch { return &nopBatch{} }

func (n nopRuleEngine) EvaluateRules(b Batch, ts time.Time) ([]*Sample, error) { return nil, nil }

type nopBatch struct{}

func (b *nopBatch) Select(sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	return nil
}

func (b *nopBatch) LabelValues(name string, matchers ...*labels.Matcher) ([]string, storage.Warnings, error) {
	return nil, nil, nil
}

func (b *nopBatch) LabelNames(matchers ...*labels.Matcher) ([]string, storage.Warnings, error) {
	return nil, nil, nil
}
func (b *nopBatch) Close() error                            { return nil }
func (b *nopBatch) Add(l labels.Labels, t int64, v float64) {}
