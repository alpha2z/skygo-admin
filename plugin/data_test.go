package plugin

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestDataContract(t *testing.T) {
	d := DataSet{ID: "queue", Title: "Queue", Modes: []string{"current", "series", "rows"}, Dimensions: []string{"region"}, Fields: []DataField{{ID: "count", Title: "Count", Type: "number"}}}
	c := DataCatalog{Version: 1, Datasets: []DataSet{d}, Panels: []DataPanel{{ID: "overview", Title: "Overview", Widgets: []DataWidget{{Dataset: "queue", Kind: "card"}}}}}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.Version = 2
	if c.Validate() == nil {
		t.Fatal("unknown version accepted")
	}
	for _, q := range []url.Values{{"sql": {"select"}}, {"filter.uid": {"1"}}, {"group": {"uid"}}, {"limit": {"101"}}, {"sort": {"secret"}}, {"limit": {"1", "2"}}} {
		if ValidateDataQuery(d, "rows", q) == nil {
			t.Fatalf("accepted %v", q)
		}
	}
	q := url.Values{"start": {"2026-01-01T00:00:00Z"}, "end": {"2026-01-01T01:00:00Z"}, "step": {"15"}}
	if e := ValidateDataQuery(d, "series", q); e != nil {
		t.Fatal(e)
	}
	q.Set("step", "1")
	if ValidateDataQuery(d, "series", q) == nil {
		t.Fatal("unbounded series")
	}
	now := time.Now()
	zero := 0.0
	r := DataResult{Version: 1, SampledAt: &now, State: "partial", Warnings: []string{}, Values: []DataValue{{Value: &zero}, {Value: nil}}}
	raw, _ := json.Marshal(r)
	var decoded DataResult
	if e := DecodeData(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	if decoded.Values[0].Value == nil || *decoded.Values[0].Value != 0 || decoded.Values[1].Value != nil {
		t.Fatal("zero and missing conflated")
	}
	if e := decoded.Validate(d, "current"); e != nil {
		t.Fatal(e)
	}
	if DecodeData([]byte(`{"version":1,"script":"bad"}`), &DataResult{}) == nil {
		t.Fatal("unknown field accepted")
	}
	r.Rows = []map[string]any{{"secret": "x"}}
	r.Values = nil
	if r.Validate(d, "rows") == nil {
		t.Fatal("unknown row field")
	}
}

func TestDataResultLimitsAndMalformedRows(t *testing.T) {
	now := time.Now()
	d := DataSet{ID: "queue", Title: "Queue", Fields: []DataField{{ID: "count", Title: "Count", Type: "number"}}}
	r := DataResult{Version: 1, State: "ok", SampledAt: &now, Warnings: []string{}, Series: make([]DataSeries, 11)}
	if r.Validate(d, "series") == nil {
		t.Fatal("excess series accepted")
	}
	r.Series = []DataSeries{{Points: make([]DataPoint, 1001)}}
	if r.Validate(d, "series") == nil {
		t.Fatal("excess points accepted")
	}
	r.Series = nil
	r.Rows = []map[string]any{{"count": "bad"}}
	if r.Validate(d, "rows") == nil {
		t.Fatal("malformed number accepted")
	}
	r.Rows = nil
	r.SampledAt = nil
	if r.Validate(d, "current") == nil {
		t.Fatal("healthy result without sample accepted")
	}
}
