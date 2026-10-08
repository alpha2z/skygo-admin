package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DataVersion is the version of the declarative data contract.
const DataVersion = 1

// DataPanel declares a presentation composed of dataset widgets.
type DataPanel struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Widgets []DataWidget `json:"widgets"`
}

// DataWidget binds one dataset to a built-in presentation kind.
type DataWidget struct {
	Dataset string `json:"dataset"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
}

// DataField describes a typed table column.
type DataField struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

// DataSet declares supported query modes and allowed dimensions.
type DataSet struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Unit       string      `json:"unit"`
	Modes      []string    `json:"modes"`
	Dimensions []string    `json:"dimensions"`
	Fields     []DataField `json:"fields,omitempty"`
}

// DataCatalog describes the datasets and panels exposed by one provider.
type DataCatalog struct {
	Version  int         `json:"version"`
	Datasets []DataSet   `json:"datasets"`
	Panels   []DataPanel `json:"panels"`
}

// DataValue is a current value; nil means unknown, not zero.
type DataValue struct {
	Labels map[string]string `json:"labels,omitempty"`
	Value  *float64          `json:"value"`
}

// DataPoint is an ordered evaluation point; nil breaks the rendered line.
type DataPoint struct {
	At    time.Time `json:"at"`
	Value *float64  `json:"value"`
}

// DataSeries contains one labeled historical sequence.
type DataSeries struct {
	Labels map[string]string `json:"labels,omitempty"`
	Points []DataPoint       `json:"points"`
}

// DataResult carries bounded query results and explicit freshness status.
type DataResult struct {
	Version   int              `json:"version"`
	SampledAt *time.Time       `json:"sampled_at"`
	State     string           `json:"state"`
	Warnings  []string         `json:"warnings"`
	Values    []DataValue      `json:"values,omitempty"`
	Series    []DataSeries     `json:"series,omitempty"`
	Rows      []map[string]any `json:"rows,omitempty"`
	Total     int              `json:"total,omitempty"`
}

// DecodeData rejects unknown envelope fields and trailing JSON.
func DecodeData(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// Validate checks catalog references, supported kinds, and metadata bounds.
func (c DataCatalog) Validate() error {
	if c.Version != DataVersion || len(c.Datasets) > 64 || len(c.Panels) > 16 {
		return errors.New("unsupported data catalog")
	}
	sets := map[string]DataSet{}
	for _, d := range c.Datasets {
		if !identifier.MatchString(d.ID) || d.Title == "" || len(d.Title) > 160 || len(d.Unit) > 32 || len(d.Modes) == 0 || len(d.Dimensions) > 32 || len(d.Fields) > 32 {
			return errors.New("invalid dataset")
		}
		if _, ok := sets[d.ID]; ok {
			return errors.New("duplicate dataset")
		}
		seen := map[string]bool{}
		for _, m := range d.Modes {
			if (m != "current" && m != "series" && m != "rows") || seen[m] {
				return errors.New("invalid data mode")
			}
			seen[m] = true
		}
		seen = map[string]bool{}
		for _, dim := range d.Dimensions {
			if !identifier.MatchString(dim) || seen[dim] {
				return errors.New("invalid dimension")
			}
			seen[dim] = true
		}
		seen = map[string]bool{}
		for _, f := range d.Fields {
			if !identifier.MatchString(f.ID) || seen[f.ID] || f.Title == "" || (f.Type != "string" && f.Type != "number" && f.Type != "boolean") {
				return errors.New("invalid field")
			}
			seen[f.ID] = true
		}
		sets[d.ID] = d
	}
	seen := map[string]bool{}
	for _, p := range c.Panels {
		if !identifier.MatchString(p.ID) || seen[p.ID] || p.Title == "" || len(p.Widgets) > 32 {
			return errors.New("invalid panel")
		}
		seen[p.ID] = true
		for _, w := range p.Widgets {
			d, ok := sets[w.Dataset]
			if !ok {
				return errors.New("unknown widget dataset")
			}
			mode := map[string]string{"card": "current", "chart": "series", "table": "rows"}[w.Kind]
			if mode == "" || !contains(d.Modes, mode) {
				return errors.New("unsupported widget")
			}
		}
	}
	return nil
}
func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Dataset looks up an explicitly declared dataset.
func (c DataCatalog) Dataset(id string) (DataSet, bool) {
	for _, d := range c.Datasets {
		if d.ID == id {
			return d, true
		}
	}
	return DataSet{}, false
}

// ValidateDataQuery rejects arbitrary query languages and bounds response work.
func ValidateDataQuery(d DataSet, mode string, q url.Values) error {
	if !contains(d.Modes, mode) {
		return errors.New("unsupported query mode")
	}
	for k, vs := range q {
		if len(vs) != 1 || len(vs[0]) > 256 {
			return errors.New("invalid query value")
		}
		if strings.HasPrefix(k, "filter.") {
			if !contains(d.Dimensions, strings.TrimPrefix(k, "filter.")) {
				return errors.New("unknown filter")
			}
			continue
		}
		switch k {
		case "group":
			if !contains(d.Dimensions, vs[0]) {
				return errors.New("unknown group")
			}
		case "start", "end", "step":
			if mode != "series" {
				return errors.New("unexpected range")
			}
		case "page", "limit", "sort", "order":
			if mode != "rows" {
				return errors.New("unexpected table query")
			}
		default:
			return errors.New("unknown query parameter")
		}
	}
	if mode == "series" {
		start, e := time.Parse(time.RFC3339, q.Get("start"))
		if e != nil {
			return e
		}
		end, e := time.Parse(time.RFC3339, q.Get("end"))
		if e != nil {
			return e
		}
		step, e := strconv.Atoi(q.Get("step"))
		if e != nil || step < 1 || step > 1296000 || end.Before(start) || end.Sub(start) > 15*24*time.Hour || end.Sub(start)/time.Second/time.Duration(step)+1 > 1000 {
			return errors.New("invalid or excessive range")
		}
	}
	if mode == "rows" {
		for k, max := range map[string]int{"page": 100000, "limit": 100} {
			if q.Get(k) != "" {
				n, e := strconv.Atoi(q.Get(k))
				if e != nil || n < 1 || n > max {
					return errors.New("invalid pagination")
				}
			}
		}
		if q.Get("sort") != "" {
			found := false
			for _, f := range d.Fields {
				found = found || f.ID == q.Get("sort")
			}
			if !found {
				return errors.New("unknown sort field")
			}
		}
		if o := q.Get("order"); o != "" && o != "asc" && o != "desc" {
			return errors.New("invalid sort order")
		}
	}
	return nil
}

// Validate checks result shape against its declared dataset and query mode.
func (r DataResult) Validate(d DataSet, mode string) error {
	if r.Version != DataVersion || !contains([]string{"ok", "partial", "stale", "unavailable"}, r.State) || len(r.Warnings) > 32 {
		return errors.New("invalid data status")
	}
	if r.State == "ok" && r.SampledAt == nil {
		return errors.New("missing sample time")
	}
	if len(r.Values) > 100 || len(r.Series) > 10 || len(r.Rows) > 100 || r.Total < 0 {
		return errors.New("data limit exceeded")
	}
	if mode != "current" && len(r.Values) > 0 || mode != "series" && len(r.Series) > 0 || mode != "rows" && len(r.Rows) > 0 {
		return errors.New("unexpected result shape")
	}
	labels := func(ls map[string]string) error {
		for k, v := range ls {
			if !contains(d.Dimensions, k) || len(v) > 256 {
				return errors.New("invalid data labels")
			}
		}
		return nil
	}
	for _, v := range r.Values {
		if err := labels(v.Labels); err != nil {
			return err
		}
	}
	for _, s := range r.Series {
		if len(s.Points) > 1000 {
			return errors.New("series limit exceeded")
		}
		if e := labels(s.Labels); e != nil {
			return e
		}
		var prev time.Time
		for _, p := range s.Points {
			if p.At.IsZero() || !prev.IsZero() && !p.At.After(prev) {
				return errors.New("unordered data points")
			}
			prev = p.At
		}
	}
	for _, row := range r.Rows {
		for k, v := range row {
			found := false
			for _, f := range d.Fields {
				if f.ID != k {
					continue
				}
				found = true
				if v == nil {
					break
				}
				valid := false
				switch f.Type {
				case "string":
					_, valid = v.(string)
				case "number":
					_, valid = v.(float64)
				case "boolean":
					_, valid = v.(bool)
				}
				if !valid {
					return fmt.Errorf("invalid field type: %s", k)
				}
			}
			if !found {
				return errors.New("unknown row field")
			}
		}
	}
	return nil
}
