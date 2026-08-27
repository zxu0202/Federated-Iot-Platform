package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

func TestAlarmQueryBindsOffsetAwareFiltersToCursor(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/simulations/run_alarm/alarms?agent=1&from=2026-08-17T08%3A00%3A00%2B08%3A00&to=2026-08-17T09%3A00%3A00%2B08%3A00&level=Warning&type=LOAD", nil)
	query, binding, err := parseAlarmQuery(request, "run_alarm")
	if err != nil {
		t.Fatal(err)
	}
	if query.From == nil || query.To == nil || query.From.UTC().Format(time.RFC3339Nano) != "2026-08-17T00:00:00Z" || query.To.UTC().Format(time.RFC3339Nano) != "2026-08-17T01:00:00Z" {
		t.Fatalf("offset-aware alarm filter was not parsed as an ISO-8601 instant: %#v", query)
	}
	cursor, err := encodeIndexedCursor("alarms.v1", binding, 12)
	if err != nil {
		t.Fatal(err)
	}
	changedFilter := httptest.NewRequest("GET", "/api/v1/simulations/run_alarm/alarms?agent=1&from=2026-08-17T08%3A00%3A00%2B08%3A00&to=2026-08-17T09%3A00%3A00%2B08%3A00&level=Alarm&type=LOAD&cursor="+cursor, nil)
	if _, _, err := parseAlarmQuery(changedFilter, "run_alarm"); err == nil {
		t.Fatal("alarm cursor was accepted after its level filter changed")
	}
}

func TestAlarmListResponsePreservesFieldsAndFilteredTotal(t *testing.T) {
	timeValue := time.Date(2026, 8, 17, 8, 30, 45, 123000000, time.FixedZone("UTC+08", 8*60*60))
	page := postgres.AlarmPage{
		Items: []postgres.AlarmRecord{{
			AlarmID: 5, RunID: "run_alarm", Agent: 1, OriginalRunningIndex: 14059, Time: &timeValue,
			OverallAlarmLevel: "Warning", AlarmType: "LOAD", Reasons: json.RawMessage(`["load threshold","spike"]`),
			LoadStatus: "Heavy load", ResultLocator: json.RawMessage(`{"agent":1,"original_running_index":14059,"artifact":"alarms.csv","row":42,"byte_offset":512}`),
		}},
		HasMore: true, Total: 1585,
	}
	request := httptest.NewRequest("GET", "/api/v1/simulations/run_alarm/alarms?agent=1", nil)
	response, err := alarmListResponse(request, "run_alarm", 1, "bound-filter", page)
	if err != nil {
		t.Fatal(err)
	}
	data := response["data"].(map[string]any)
	meta := response["meta"].(map[string]any)
	if data["total"] != int64(1585) || meta["total"] != int64(1585) || data["has_more"] != true || data["next_cursor"] == nil {
		t.Fatalf("alarm response lacks its authoritative filtered total/paging fields: %#v", response)
	}
	item := data["items"].([]any)[0].(map[string]any)
	if item["time"] != "2026-08-17T00:30:45.123Z" || item["original_running_index"] != int64(14059) {
		t.Fatalf("alarm identity/time is not offset-aware ISO-8601: %#v", item)
	}
	reasons := item["reasons"].([]any)
	locator := item["result_locator"].(map[string]any)
	if len(reasons) != 2 || reasons[0] != "load threshold" || locator["agent"] != 1 || locator["original_running_index"] != int64(14059) || len(locator) != 2 {
		t.Fatalf("alarm reasons or safe result locator were not preserved: %#v", item)
	}
	empty, err := alarmListResponse(request, "run_alarm", 1, "bound-filter", postgres.AlarmPage{Items: []postgres.AlarmRecord{}, Total: 0})
	if err != nil {
		t.Fatal(err)
	}
	emptyData := empty["data"].(map[string]any)
	if emptyData["total"] != int64(0) || emptyData["next_cursor"] != nil || emptyData["has_more"] != false {
		t.Fatalf("empty alarm page omits stable pagination fields: %#v", empty)
	}
}
