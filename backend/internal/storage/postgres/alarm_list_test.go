package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestAlarmListWhereSharesEveryFilterButExcludesCursor(t *testing.T) {
	from := time.Date(2026, 8, 17, 8, 0, 0, 0, time.FixedZone("UTC+08", 8*60*60))
	to := from.Add(time.Hour)
	indexFrom, indexTo := int64(14059), int64(14100)
	where, args := alarmListWhere(AlarmQuery{
		RunID: "run_alarm", Agent: 1, Levels: []string{"Warning", "Alarm"}, Types: []string{"LOAD", "QUALITY"},
		From: &from, To: &to, IndexFrom: &indexFrom, IndexTo: &indexTo, AfterID: 99,
	})
	for _, expected := range []string{
		"run_id=$1", "agent=$2", "overall_alarm_level = ANY($3)", "alarm_type = ANY($4)",
		"time_value >= $5", "time_value <= $6", "original_running_index >= $7", "original_running_index <= $8",
	} {
		if !strings.Contains(where, expected) {
			t.Fatalf("shared alarm filter plan is missing %q: %s", expected, where)
		}
	}
	if strings.Contains(where, "alarm_id >") || len(args) != 8 {
		t.Fatalf("cursor boundary leaked into total filter: where=%s args=%d", where, len(args))
	}
	if actual := args[4].(time.Time); !actual.Equal(from.UTC()) {
		t.Fatalf("offset-aware alarm lower bound = %s, want %s", actual, from.UTC())
	}
}

func TestAlarmPageRetainsFilteredTotalOnEveryPage(t *testing.T) {
	records := make([]AlarmRecord, 5)
	for index := range records {
		records[index].AlarmID = int64(index + 1)
	}
	first := alarmPageFromRecords(records[:3], 2, 5)
	intermediate := alarmPageFromRecords(records[2:], 2, 5)
	last := alarmPageFromRecords(records[4:], 2, 5)
	empty := alarmPageFromRecords(nil, 2, 0)
	cursorEmpty := alarmPageFromRecords(nil, 2, 5)
	for name, page := range map[string]AlarmPage{"first": first, "intermediate": intermediate, "last": last, "empty": empty, "cursor_empty": cursorEmpty} {
		if name == "empty" {
			if page.Total != 0 || len(page.Items) != 0 || page.HasMore {
				t.Fatalf("%s page = %#v, want empty filtered result", name, page)
			}
			continue
		}
		if page.Total != 5 {
			t.Fatalf("%s page total = %d, want filtered total 5", name, page.Total)
		}
	}
	if len(first.Items) != 2 || !first.HasMore || len(intermediate.Items) != 2 || !intermediate.HasMore || len(last.Items) != 1 || last.HasMore || len(cursorEmpty.Items) != 0 || cursorEmpty.HasMore {
		t.Fatalf("unexpected first/intermediate/final pagination: first=%#v intermediate=%#v last=%#v empty=%#v", first, intermediate, last, cursorEmpty)
	}
	seen := map[int64]bool{}
	for _, page := range []AlarmPage{first, intermediate, last} {
		for _, item := range page.Items {
			if seen[item.AlarmID] {
				t.Fatalf("cursor pages repeated alarm %d", item.AlarmID)
			}
			seen[item.AlarmID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("cursor pages omitted alarms: saw %d, want 5", len(seen))
	}
}
