package archive

import (
	"reflect"
	"testing"
	"time"
)

func utcDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func TestDaysToArchiveSelectsDueDays(t *testing.T) {
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	today := utcDay(2026, time.November, 16)
	daysAgo := func(age int) time.Time { return today.AddDate(0, 0, -age) }
	withRows := func(ages ...int) map[string]bool {
		days := map[string]bool{}
		for _, age := range ages {
			days[dayKey(daysAgo(age))] = true
		}
		return days
	}

	tests := map[string]struct {
		now            time.Time
		daysWithRows   map[string]bool
		lastArchivedAt map[string]time.Time
		expected       []DueDay
	}{
		"complete days come before partial days": {
			now:          now,
			daysWithRows: withRows(20, 3),
			expected:     []DueDay{{Day: daysAgo(3)}, {Day: daysAgo(20), Partial: true}},
		},
		"complete days are oldest first": {
			now:          now,
			daysWithRows: withRows(3, 5, 4),
			expected:     []DueDay{{Day: daysAgo(5)}, {Day: daysAgo(4)}, {Day: daysAgo(3)}},
		},
		"partial days are newest first": {
			now:          now,
			daysWithRows: withRows(30, 20, 25),
			expected: []DueDay{
				{Day: daysAgo(20), Partial: true},
				{Day: daysAgo(25), Partial: true},
				{Day: daysAgo(30), Partial: true},
			},
		},
		"day aged 13.9 days is complete": {
			now:          daysAgo(14).AddDate(0, 0, 14).Add(-2*time.Hour - 24*time.Minute),
			daysWithRows: map[string]bool{dayKey(daysAgo(14)): true},
			expected:     []DueDay{{Day: daysAgo(14)}},
		},
		"day aged exactly 14 days is partial": {
			now:          daysAgo(14).AddDate(0, 0, 14),
			daysWithRows: map[string]bool{dayKey(daysAgo(14)): true},
			expected:     []DueDay{{Day: daysAgo(14), Partial: true}},
		},
		"window includes today minus 58 and excludes today minus 59": {
			now:          now,
			daysWithRows: withRows(59, 58),
			expected:     []DueDay{{Day: daysAgo(58), Partial: true}},
		},
		"today and yesterday are never returned": {
			now:          now,
			daysWithRows: withRows(0, 1),
			expected:     nil,
		},
		"day without rows is never returned": {
			now:          now,
			daysWithRows: withRows(),
			expected:     nil,
		},
		"archived day is not returned before the second pass": {
			now:            now,
			daysWithRows:   withRows(5),
			lastArchivedAt: map[string]time.Time{dayKey(daysAgo(5)): daysAgo(5).AddDate(0, 0, 3)},
			expected:       nil,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			actual := DaysToArchive(test.now, test.daysWithRows, test.lastArchivedAt)
			if !reflect.DeepEqual(actual, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, actual)
			}
		})
	}
}

func TestDaysToArchiveRunsSecondPassOnce(t *testing.T) {
	day := utcDay(2026, time.November, 1)
	daysWithRows := map[string]bool{dayKey(day): true}
	archivedAtDayThree := map[string]time.Time{dayKey(day): day.AddDate(0, 0, 3)}

	tests := map[string]struct {
		now            time.Time
		lastArchivedAt map[string]time.Time
		expected       []DueDay
	}{
		"returned once the day is twelve days old": {
			now:            day.AddDate(0, 0, 12),
			lastArchivedAt: archivedAtDayThree,
			expected:       []DueDay{{Day: day}},
		},
		"not returned before twelve days": {
			now:            day.AddDate(0, 0, 12).Add(-time.Second),
			lastArchivedAt: archivedAtDayThree,
		},
		"not returned when already archived after twelve days": {
			now:            day.AddDate(0, 0, 13),
			lastArchivedAt: map[string]time.Time{dayKey(day): day.AddDate(0, 0, 12)},
		},
		"not returned once the success ttl has passed": {
			now:            day.AddDate(0, 0, 14),
			lastArchivedAt: archivedAtDayThree,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			actual := DaysToArchive(test.now, daysWithRows, test.lastArchivedAt)
			if !reflect.DeepEqual(actual, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, actual)
			}
		})
	}
}
