package domain

import (
	"sort"
	"time"
)

const infiniteSafetyMargin = time.Duration(1<<63 - 1)

func RankCandidates(commute Commute, candidates []Itinerary) []Itinerary {
	ranked := make([]Itinerary, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.IsEligibleFor(commute) {
			ranked = append(ranked, candidate)
		}
	}

	sort.SliceStable(ranked, func(left, right int) bool {
		return ranksBefore(commute, ranked[left], ranked[right])
	})
	return ranked
}

func ranksBefore(commute Commute, left, right Itinerary) bool {
	if left.Duration() != right.Duration() {
		return left.Duration() < right.Duration()
	}
	if left.Transfers() != right.Transfers() {
		return left.Transfers() < right.Transfers()
	}
	leftTotalGap, leftMinimumMargin := transferGapMetrics(commute, left)
	rightTotalGap, rightMinimumMargin := transferGapMetrics(commute, right)
	if leftTotalGap != rightTotalGap {
		return leftTotalGap < rightTotalGap
	}
	return leftMinimumMargin > rightMinimumMargin
}

func transferGapMetrics(commute Commute, itinerary Itinerary) (time.Duration, time.Duration) {
	var totalGap time.Duration
	minimumMargin := infiniteSafetyMargin
	for _, transfer := range itinerary.transitTransfers() {
		totalGap += transfer.gap
		margin := transfer.gap - commute.saintLazareMinimumTransferTime.duration
		if margin < minimumMargin {
			minimumMargin = margin
		}
	}
	return totalGap, minimumMargin
}
