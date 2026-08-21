package app

import (
	"fmt"
	"time"

	"mind-the-time/internal/domain"
)

func InputFromCommute(commute domain.Commute) CommuteInput {
	weekdays := make([]time.Weekday, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		if commute.Schedule().IsActive(day) {
			weekdays = append(weekdays, day)
		}
	}
	return CommuteInput{
		Origin:                     commute.Origin().Text(),
		Destination:                commute.Destination().Text(),
		DepartureTime:              commute.Schedule().DepartureTime().String(),
		Weekdays:                   weekdays,
		MaximumInitialWalkMinutes:  fmt.Sprintf("%d", int(commute.MaximumInitialWalk().Duration().Minutes())),
		SaintLazareTransferMinutes: fmt.Sprintf("%d", int(commute.SaintLazareMinimumTransferTime().Duration().Minutes())),
		PreferredLines:             commute.PreferredLines().Raw(),
	}
}

func InputFromCommuteRecord(record CommuteRecord) CommuteInput {
	input := InputFromCommute(record.Commute)
	input.Name = record.Name.Text()
	return input
}
