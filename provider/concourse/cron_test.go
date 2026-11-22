package concourse

import (
	"reflect"
	"testing"
)

// The time resource keys are interval, start, stop, days and location (concourse/time-resource v1.11.6 README).
func TestScheduleSourceTable(t *testing.T) {
	type src = map[string]any
	for _, c := range []struct {
		want src // nil means lost
		cron string
	}{
		{src{"interval": "1m"}, "* * * * *"},
		{src{"interval": "5m"}, "*/5 * * * *"},
		{src{"interval": "15m"}, "*/15 * * * *"},
		{src{"interval": "1h"}, "0 * * * *"},
		{src{"interval": "6h"}, "0 */6 * * *"},
		{src{"interval": "1h"}, "0 */1 * * *"},
		{src{}, "0 0 * * *"},
		{src{"start": "02:30", "stop": "02:31"}, "30 2 * * *"},
		{src{"start": "23:59", "stop": "00:00"}, "59 23 * * *"},
		{src{"days": []string{"Monday", "Wednesday"}}, "0 0 * * 1,3"},
		{src{"days": []string{"Sunday"}}, "0 0 * * 7"},
		{src{}, "0 0 * * 0-6"},
		{src{"days": []string{"Sunday", "Saturday"}}, "0 0 * * SAT,sun"},
		{src{"start": "02:30", "stop": "02:31", "days": []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday"}}, "30 2 * * MON-FRI"},
		{src{"start": "04:15", "stop": "04:16", "days": []string{"Tuesday"}}, "15 4 * * 2"},
		{nil, "*/7 * * * *"},
		{nil, "*/60 * * * *"},
		{nil, "0 */5 * * *"},
		{nil, "0 */24 * * *"},
		{nil, "15,45 * * * *"},
		{nil, "0 9-17 * * *"},
		{nil, "*/5 9 * * *"},
		{nil, "0 0 1 * *"},
		{nil, "0 0 * JAN *"},
		{nil, "*/5 * * * 1"},
		{nil, "0 0 * * 1-"},
		{nil, "0 0 * * 5-1"},
		{nil, "0 0 * * 8"},
		{nil, "60 0 * * *"},
		{nil, "0 24 * * *"},
		{nil, "0 0 * * "},
		{nil, "@daily"},
		{nil, "0 0 0 * * *"},
		{nil, "H H * * *"},
		{nil, ""},
	} {
		got, ok := scheduleSource(c.cron)
		if c.want == nil {
			if ok || got != nil {
				t.Errorf("%q = %v, %v; want lost", c.cron, got, ok)
			}
			continue
		}
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %v, %v; want %v", c.cron, got, ok, c.want)
		}
	}
}
