package domain

import (
	"errors"
	"time"
)

var ErrInvalidSchedule = errors.New("invalid weekly schedule")

var ErrInvalidDepartureTime = errors.New("invalid departure time")

type DepartureTime struct {
	hour   uint8
	minute uint8
	valid  bool
}

func NewDepartureTime(value string) (DepartureTime, error) {
	if len(value) != 5 || value[2] != ':' || !isASCIIDigit(value[0]) || !isASCIIDigit(value[1]) || !isASCIIDigit(value[3]) || !isASCIIDigit(value[4]) {
		return DepartureTime{}, ErrInvalidDepartureTime
	}
	hour := uint8((value[0]-'0')*10 + value[1] - '0')
	minute := uint8((value[3]-'0')*10 + value[4] - '0')
	if hour > 23 || minute > 59 {
		return DepartureTime{}, ErrInvalidDepartureTime
	}
	return DepartureTime{hour: hour, minute: minute, valid: true}, nil
}

func (d DepartureTime) String() string {
	return string([]byte{'0' + d.hour/10, '0' + d.hour%10, ':', '0' + d.minute/10, '0' + d.minute%10})
}

func (d DepartureTime) Hour() int {
	return int(d.hour)
}

func (d DepartureTime) Minute() int {
	return int(d.minute)
}

func (d DepartureTime) isValid() bool {
	return d.valid && d.hour <= 23 && d.minute <= 59
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

type WeeklySchedule struct {
	active        [7]bool
	departureTime DepartureTime
}

func NewWeeklySchedule(days []time.Weekday, departureTime DepartureTime) (WeeklySchedule, error) {
	if len(days) == 0 || !departureTime.isValid() {
		return WeeklySchedule{}, ErrInvalidSchedule
	}

	var schedule WeeklySchedule
	for _, day := range days {
		if day < time.Sunday || day > time.Saturday {
			return WeeklySchedule{}, ErrInvalidSchedule
		}
		schedule.active[day] = true
	}
	schedule.departureTime = departureTime
	return schedule, nil
}

func (s WeeklySchedule) IsActive(day time.Weekday) bool {
	return day >= time.Sunday && day <= time.Saturday && s.active[day]
}

func (s WeeklySchedule) DepartureTime() DepartureTime {
	return s.departureTime
}

func (s WeeklySchedule) DepartureAt(serviceDate time.Time) (time.Time, error) {
	if !s.isValid() || serviceDate.IsZero() {
		return time.Time{}, ErrInvalidSchedule
	}
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.Time{}, err
	}
	year, month, day := serviceDate.Date()
	return time.Date(year, month, day, int(s.departureTime.hour), int(s.departureTime.minute), 0, 0, paris), nil
}

func (s WeeklySchedule) isValid() bool {
	if !s.departureTime.isValid() {
		return false
	}
	for _, active := range s.active {
		if active {
			return true
		}
	}
	return false
}
