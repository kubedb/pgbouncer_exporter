/*
Copyright 2019 The KubeDB Authors.
Copyright (c) 2017 Kristoffer K Larsen <kristoffer@larsen.so>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://opensource.org/licenses/MIT

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

//NewExporter creates a new exporter in a namespace for a given connection string of a pgbouncer server. namespace is always pgbouncer
func NewExporter(connectionString string, namespace string) *Exporter {

	db, err := getDB(connectionString)

	if err != nil {
		log.Fatal(err)
	}

	return &Exporter{
		metricMap:        makeMetricMaps(namespace),
		namespace:        namespace,
		db:               db,
		connectionString: connectionString,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Was the PgBouncer instance query successful?",
		}),

		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from PgBouncer.",
		}),

		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrapes_total",
			Help:      "Total number of times PgBouncer has been scraped for metrics.",
		}),

		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics from PgBouncer resulted in an error (1 for error, 0 for success).",
		}),
	}
}

// Query within a namespace mapping and emit metrics. Returns fatal errors if
func getDB(conn string) (*sql.DB, error) {
	// Note we use OpenDB so we can still create the connector even if the backend is down.
	connector, err := pq.NewConnector(conn)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	return db, nil
}

//makeMetricMaps returns a single array of maps containing metic maps for each metric from each group of metrics (ie kv,row)
func makeMetricMaps(metricNamespace string) []*MetricMapFromNamespace {
	var metricMap []*MetricMapFromNamespace

	convert := func(namespace string, mappings map[string]ColumnMapping, converter RowConverter) *MetricMapFromNamespace {
		thisMap := make(map[string]MetricMap)

		labels := []string{}
		for columnName, columnMapping := range mappings {
			if columnMapping.usage == LABEL {
				labels = append(labels, columnName)
			}
		}
		for columnName, columnMapping := range mappings {
			// Determine how to convert the column based on its usage.
			desc := prometheus.NewDesc(fmt.Sprintf("%s_%s_%s", metricNamespace, namespace, columnName), columnMapping.description, labels, nil)
			if columnMapping.promMetricName != "" {
				desc = prometheus.NewDesc(fmt.Sprintf("%s_%s_%s", metricNamespace, namespace, columnMapping.promMetricName), columnMapping.description, labels, nil)
			}

			switch columnMapping.usage {
			case COUNTER:
				thisMap[columnName] = MetricMap{
					vtype:      prometheus.CounterValue,
					desc:       desc,
					multiplier: 1,
				}
			case GAUGE:
				thisMap[columnName] = MetricMap{
					vtype:      prometheus.GaugeValue,
					desc:       desc,
					multiplier: 1,
				}
			case GAUGE_MS:
				thisMap[columnName] = MetricMap{
					vtype:      prometheus.GaugeValue,
					desc:       desc,
					multiplier: 1e-6,
				}
			}
		}
		return &MetricMapFromNamespace{namespace: namespace, columnMappings: thisMap, labels: labels, rowFunc: converter}
	}

	for namespace, mappings := range metricRowMaps {
		metricMap = append(metricMap, convert(namespace, mappings, metricRowConverter))
	}
	for namespace, mappings := range metricKVMaps {
		metricMap = append(metricMap, convert(namespace, mappings, metricKVConverter))
	}
	return metricMap
}

func metricRowConverter(m *MetricMapFromNamespace, result *rowResult, ch chan<- prometheus.Metric) ([]error, error) {
	var nonFatalErrors []error
	labelValues := []string{}
	// collect label data first.
	for _, name := range m.labels {
		val := result.ColumnData[result.ColumnIdx[name]]
		if val == nil {
			labelValues = append(labelValues, "")
		} else if v, ok := val.(string); ok {
			labelValues = append(labelValues, v)
		} else if v, ok := val.(int64); ok {
			labelValues = append(labelValues, strconv.FormatInt(v, 10))
		}
	}

	for idx, columnName := range result.ColumnNames {
		if metricMapping, ok := m.columnMappings[columnName]; ok {
			value, ok := dbToFloat64(result.ColumnData[idx])
			if !ok {
				nonFatalErrors = append(nonFatalErrors, errors.New(fmt.Sprintln("Unexpected error parsing column: ", m.namespace, columnName, result.ColumnData[idx])))
				continue
			}
			log.Debugln("successfully parsed column:", m.namespace, columnName, result.ColumnData[idx])
			// Generate the metric
			ch <- prometheus.MustNewConstMetric(metricMapping.desc, metricMapping.vtype, value*metricMapping.multiplier, labelValues...)
		} else {
			log.Debugln("Ignoring column for metric conversion:", m.namespace, columnName)
		}
	}
	return nonFatalErrors, nil
}

func metricKVConverter(m *MetricMapFromNamespace, result *rowResult, ch chan<- prometheus.Metric) ([]error, error) {
	// format is key, value, <ignorable> for row results.
	if len(result.ColumnData) < 2 {
		return nil, errors.New(fmt.Sprintln("Received row results for KV parsing, but not enough columns; something is deeply broken:", m.namespace, result.ColumnData))
	}
	var key string
	switch v := result.ColumnData[0].(type) {
	case string:
		key = v
	default:
		return nil, errors.New(fmt.Sprintln("Received row results for KV parsing, but key field isn't string:", m.namespace, result.ColumnData))
	}
	// is it a key we care about?
	if metricMapping, ok := m.columnMappings[key]; ok {
		value, ok := dbToFloat64(result.ColumnData[1])
		if !ok {
			return append([]error{}, errors.New(fmt.Sprintln("Unexpected error KV value: ", m.namespace, key, result.ColumnData[1]))), nil
		}
		log.Debugln("successfully parsed column:", m.namespace, key, result.ColumnData[1])
		// Generate the metric
		ch <- prometheus.MustNewConstMetric(metricMapping.desc, metricMapping.vtype, value*metricMapping.multiplier)
	} else {
		log.Debugln("Ignoring column for KV conversion:", m.namespace, key)
	}
	return nil, nil
}

// Convert database.sql types to float64s for Prometheus consumption. Null types are mapped to NaN. string and []byte
// types are mapped as NaN and !ok
func dbToFloat64(t interface{}) (float64, bool) {
	switch v := t.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case time.Time:
		return float64(v.Unix()), true
	case []byte:
		// Try and convert to string and then parse to a float64
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			return math.NaN(), false
		}
		return result, true
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			log.Infoln("Could not parse string:", err)
			return math.NaN(), false
		}
		return result, true
	case nil:
		return math.NaN(), true
	default:
		return math.NaN(), false
	}
}
