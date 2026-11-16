package archive

import (
	"sort"
	"time"
)

const (
	dayKeyLayout      = "2006-01-02"
	newestArchivedAge = 2
	oldestArchivedAge = 58
	secondPassAgeDays = 12
	successTTLAgeDays = 14
)

type DueDay struct {
	Day     time.Time
	Partial bool
}

func dayKey(day time.Time) string {
	return day.Format(dayKeyLayout)
}

func DaysToArchive(now time.Time, daysWithRows map[string]bool, lastArchivedAt map[string]time.Time) []DueDay {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var completeDays, secondPassDays, partialDays []DueDay
	for age := oldestArchivedAge; age >= newestArchivedAge; age-- {
		day := today.AddDate(0, 0, -age)
		key := dayKey(day)
		if !daysWithRows[key] {
			continue
		}
		successTTLEnd := day.AddDate(0, 0, successTTLAgeDays)
		secondPassStart := day.AddDate(0, 0, secondPassAgeDays)
		archivedAt, alreadyArchived := lastArchivedAt[key]
		switch {
		case !alreadyArchived && now.Before(successTTLEnd):
			completeDays = append(completeDays, DueDay{Day: day})
		case !alreadyArchived:
			partialDays = append(partialDays, DueDay{Day: day, Partial: true})
		case archivedAt.Before(secondPassStart) && !now.Before(secondPassStart) && now.Before(successTTLEnd):
			secondPassDays = append(secondPassDays, DueDay{Day: day})
		}
	}
	sort.Slice(partialDays, func(first, second int) bool {
		return partialDays[first].Day.After(partialDays[second].Day)
	})
	dueDays := append(completeDays, secondPassDays...)
	return append(dueDays, partialDays...)
}
