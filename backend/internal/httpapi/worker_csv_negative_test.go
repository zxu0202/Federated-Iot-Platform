package httpapi

import (
	"strings"
	"testing"
)

func TestWorkerCSVReadersRejectMalformedHeadersAndRows(t *testing.T) {
	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "malformed metadata header",
			read: func() error {
				_, err := readCSVMetadataRows(strings.NewReader("Agent,\x00Metric\nAgent 1,1\n"), 3)
				return err
			},
		},
		{
			name: "duplicate result header",
			read: func() error {
				_, err := readResultCSVPage(strings.NewReader("OriginalRunningIndex,Time,Agent,Agent\n1,2026-08-16 08:00:00,1,1\n"), "run_csv", resultQuery{Agent: 1, Limit: 1, Sort: "index_asc", Binding: "csv-negative"}, "sha")
				return err
			},
		},
		{
			name: "missing result identity column",
			read: func() error {
				_, err := readResultCSVPage(strings.NewReader("OriginalRunningIndex,Agent\n1,1\n"), "run_csv", resultQuery{Agent: 1, Limit: 1, Sort: "index_asc", Binding: "csv-negative"}, "sha")
				return err
			},
		},
		{
			name: "result row width changes",
			read: func() error {
				_, err := readResultCSVPage(strings.NewReader("OriginalRunningIndex,Time,Agent\n1,2026-08-16 08:00:00,1\n2,2026-08-16 08:00:01,1,unexpected\n"), "run_csv", resultQuery{Agent: 1, Limit: 2, Sort: "index_asc", Binding: "csv-negative"}, "sha")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.read(); err == nil {
				t.Fatal("malformed Worker CSV was accepted")
			}
		})
	}
}

func TestWorkerCSVReaderRetainsHeaderForLongRowsAndBothPageOrders(t *testing.T) {
	longValue := strings.Repeat("x", 128*1024)
	contents := "OriginalRunningIndex,Time,Agent,Diagnostic\n" +
		"14059,2026-08-16 08:30:00,1," + longValue + "\n" +
		"14060,2026-08-16 08:30:01,1,normal\n" +
		"14061,2026-08-16 08:30:02,1,normal\n"
	query := resultQuery{Agent: 1, Limit: 2, Sort: "index_asc", Binding: "csv-long"}
	ascending, err := readResultCSVPage(strings.NewReader(contents), "run_csv", query, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(ascending.items) != 2 || !ascending.hasMore || ascending.items[0]["OriginalRunningIndex"] != int64(14059) || ascending.items[0]["Diagnostic"] != longValue {
		t.Fatalf("long Worker row corrupted the immutable result header or ascending page: %#v", ascending)
	}

	descending, err := readResultCSVPage(strings.NewReader(contents), "run_csv", resultQuery{Agent: 1, Limit: 2, Sort: "index_desc", Binding: "csv-long"}, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(descending.items) != 2 || !descending.hasMore || descending.items[0]["OriginalRunningIndex"] != int64(14061) || descending.items[1]["OriginalRunningIndex"] != int64(14060) {
		t.Fatalf("descending page is not stable after long Worker rows: %#v", descending)
	}
	after := int64(14060)
	final, err := readResultCSVPage(strings.NewReader(contents), "run_csv", resultQuery{Agent: 1, Limit: 2, Sort: "index_desc", Binding: "csv-long", After: &after}, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.items) != 1 || final.hasMore || final.items[0]["OriginalRunningIndex"] != int64(14059) {
		t.Fatalf("descending cursor page omitted or repeated a Worker result: %#v", final)
	}
}
