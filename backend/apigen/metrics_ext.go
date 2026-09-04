package apigen

import (
	"encoding/json"
	"math"
)

// MetricsSeries.Values uses NaN for "no data in this bucket". Protobuf carries
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

func (s MetricsSeries) MarshalJSON() ([]byte, error) {
	type plain MetricsSeries // drops the methods so the embed does not recurse
	shadow := struct {
		plain
		Values []metricsJSONValue `json:"values,omitempty"`
	}{plain: plain(s)}
	if s.Values != nil {
		shadow.Values = make([]metricsJSONValue, len(s.Values))
		for i, v := range s.Values {
			shadow.Values[i] = metricsJSONValue(v)
		}
	}
	return json.Marshal(shadow)
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
	if shadow.Values == nil {
		s.Values = nil
		return nil
	}
	s.Values = make([]float64, len(shadow.Values))
	for i, v := range shadow.Values {
		if v == nil {
			s.Values[i] = math.NaN()
		} else {
			s.Values[i] = *v
		}
	}
	return nil
}
