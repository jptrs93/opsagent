package apigen

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// A JSON client must get a response for a window with empty buckets: the
// NaN placeholder becomes null and comes back as NaN.
func TestMetricsSeriesJSONRoundTripsMissingBuckets(t *testing.T) {
	res := &MetricsQueryResponse{
		StepMs:  30000,
		Buckets: 3,
		Series: []*MetricsSeries{{
			ScheduledInstanceID: 807,
			Run:                 1,
			Field:               "cpu_usage_usec",
			Values:              []float64{1.5, math.NaN(), 2},
		}},
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	for _, want := range []string{`"values":[1.5,null,2]`, `"scheduled_instance_id":807`, `"field":"cpu_usage_usec"`, `"run":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("json %s lacks %s", body, want)
		}
	}

	var back MetricsQueryResponse
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(back.Series))
	}
	got := back.Series[0]
	if got.ScheduledInstanceID != 807 || got.Run != 1 || got.Field != "cpu_usage_usec" {
		t.Fatalf("scalar fields lost: %+v", got)
	}
	if len(got.Values) != 3 || got.Values[0] != 1.5 || !math.IsNaN(got.Values[1]) || got.Values[2] != 2 {
		t.Fatalf("values = %v, want [1.5 NaN 2]", got.Values)
	}
}

// Value and pointer forms both go through the custom marshaller, and an
// empty series keeps the generated omitempty behaviour.
func TestMetricsSeriesJSONOmitsEmptyValues(t *testing.T) {
	b, err := json.Marshal(MetricsSeries{Field: "pids"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"values"`) {
		t.Fatalf("empty values were emitted: %s", b)
	}
	b, err = json.Marshal(&MetricsSeries{Field: "pids", Values: []float64{math.Inf(1)}})
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	if !strings.Contains(string(b), `"values":[null]`) {
		t.Fatalf("infinity not rendered as null: %s", b)
	}
}
