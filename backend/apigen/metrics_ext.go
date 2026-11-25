package apigen

import (
	"encoding/json"
	"math"
)

// MetricsSeries.Values and NodeMetricsSeries.Values use NaN for "no data in this bucket". Protobuf carries
// NaN, but encoding/json refuses it ("json: unsupported value: NaN"), which
// used to turn any JSON metrics query over a partially covered window into a
// 500. The JSON rendering of a missing bucket is therefore null, and null
// decodes back to NaN so a Go JSON client sees the same series as a protobuf
// one. The generated struct stays the single source of the field layout: the
// shadow types below only swap the element type of values.

type metricsJSONValue float64

func (v metricsJSONValue) MarshalJSON() ([]byte, error) {
	f := float64(v)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}

func jsonMetricValues(values []float64) []metricsJSONValue {
	if values == nil {
		return nil
	}
	out := make([]metricsJSONValue, len(values))
	for i, v := range values {
		out[i] = metricsJSONValue(v)
	}
	return out
}

func metricValuesFromJSON(values []*float64) []float64 {
	if values == nil {
		return nil
	}
	out := make([]float64, len(values))
	for i, v := range values {
		if v == nil {
			out[i] = math.NaN()
		} else {
			out[i] = *v
		}
	}
	return out
}

func (s MetricsSeries) MarshalJSON() ([]byte, error) {
	type plain MetricsSeries // drops the methods so the embed does not recurse
	return json.Marshal(struct {
		plain
		Values []metricsJSONValue `json:"values,omitempty"`
	}{plain: plain(s), Values: jsonMetricValues(s.Values)})
}

func (s *MetricsSeries) UnmarshalJSON(b []byte) error {
	type plain MetricsSeries
	shadow := struct {
		*plain
		Values []*float64 `json:"values"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(b, &shadow); err != nil {
		return err
	}
	s.Values = metricValuesFromJSON(shadow.Values)
	return nil
}

func (s NodeMetricsSeries) MarshalJSON() ([]byte, error) {
	type plain NodeMetricsSeries // drops the methods so the embed does not recurse
	return json.Marshal(struct {
		plain
		Values []metricsJSONValue `json:"values,omitempty"`
	}{plain: plain(s), Values: jsonMetricValues(s.Values)})
}

func (s *NodeMetricsSeries) UnmarshalJSON(b []byte) error {
	type plain NodeMetricsSeries
	shadow := struct {
		*plain
		Values []*float64 `json:"values"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(b, &shadow); err != nil {
		return err
	}
	s.Values = metricValuesFromJSON(shadow.Values)
	return nil
}
