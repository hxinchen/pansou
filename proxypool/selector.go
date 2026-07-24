package proxypool

import (
	"sort"
	"strings"
)

const (
	SelectionLeastScore = "least_score"
	SelectionRoundRobin = "round_robin"
	SelectionFillFirst  = "fill_first"
)

type proxySelector interface {
	Pick(target string, candidates []*runtimeProxy) *runtimeProxy
}

type leastScoreSelector struct{}

func (leastScoreSelector) Pick(target string, candidates []*runtimeProxy) *runtimeProxy {
	var selected *runtimeProxy
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if selected == nil {
			selected = candidate
			continue
		}
		candidateScore := proxyScore(candidate, target)
		selectedScore := proxyScore(selected, target)
		if candidateScore < selectedScore || (candidateScore == selectedScore && candidate.node.ID < selected.node.ID) {
			selected = candidate
		}
	}
	return selected
}

type roundRobinSelector struct {
	cursors map[string]int
}

func (s *roundRobinSelector) Pick(target string, candidates []*runtimeProxy) *runtimeProxy {
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].node.ID < candidates[j].node.ID })
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	if len(s.cursors) >= 4096 {
		clear(s.cursors)
	}
	index := s.cursors[target]
	s.cursors[target] = index + 1
	return candidates[index%len(candidates)]
}

type fillFirstSelector struct{}

func (fillFirstSelector) Pick(_ string, candidates []*runtimeProxy) *runtimeProxy {
	if len(candidates) == 0 {
		return nil
	}
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.node.ID < selected.node.ID {
			selected = candidate
		}
	}
	return selected
}

func newProxySelector(strategy string) proxySelector {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case SelectionRoundRobin:
		return &roundRobinSelector{}
	case SelectionFillFirst:
		return fillFirstSelector{}
	default:
		return leastScoreSelector{}
	}
}

func proxyScore(node *runtimeProxy, target string) float64 {
	latency := float64(node.node.LatencyMS)
	if latency <= 0 {
		latency = 5000
	}
	total := node.node.SuccessCount + node.node.FailureCount
	successRate := 0.7
	if total > 0 {
		successRate = float64(node.node.SuccessCount+1) / float64(total+2)
	}
	if targetState := node.targets[target]; targetState != nil {
		targetTotal := targetState.successCount + targetState.failureCount
		if targetTotal > 0 {
			if targetState.latencyMS > 0 {
				latency = float64(targetState.latencyMS)
			}
			successRate = float64(targetState.successCount+1) / float64(targetTotal+2)
		}
	}
	return latency * float64(node.inflight+1) / successRate
}
