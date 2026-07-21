package app

import (
	"fmt"

	"github.com/mansiverma897993/signoz/internal/model"
	sourceprometheus "github.com/mansiverma897993/signoz/internal/source/prometheus"
)

func loadRuleSets(paths []string) ([]model.RuleSet, map[string]model.Rule, error) {
	sets := make([]model.RuleSet, 0, len(paths))
	for _, path := range paths {
		set, err := sourceprometheus.ParseFile(path)
		if err != nil {
			return nil, nil, err
		}
		sets = append(sets, set)
	}
	valid := make([]bool, len(sets))
	for index := range valid {
		valid[index] = true
	}
	recordingRules, err := recordingRuleIndex(sets, valid)
	return sets, recordingRules, err
}

func recordingRuleIndex(ruleSets []model.RuleSet, valid []bool) (map[string]model.Rule, error) {
	index := make(map[string]model.Rule)
	for setIndex, set := range ruleSets {
		if !valid[setIndex] {
			continue
		}
		for _, group := range set.Groups {
			for _, rule := range group.Rules {
				if !rule.IsRecording() {
					continue
				}
				if existing, found := index[rule.Record]; found {
					return nil, fmt.Errorf("recording rule %q is defined more than once (%s and %s)", rule.Record, existing.SourcePath, rule.SourcePath)
				}
				rule.Labels = group.EffectiveLabels(rule)
				index[rule.Record] = rule
			}
		}
	}
	return index, nil
}
