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

// the scrape fails, and a slice of errors if they were non-fatal.
func (m *MetricMapNamespace) Query(ch chan<- prometheus.Metric, db *sql.DB) ([]error, error) {
	query := fmt.Sprintf("SHOW %s;", m.namespace)

	// Don't fail on a bad scrape of one metric
	rows, err := db.Query(query)
	if err != nil {
		return []error{}, errors.New(fmt.Sprintln("Error running query on database: ", m.namespace, err))
	}

	defer rows.Close()

	var result rowResult
	result.ColumnNames, err = rows.Columns()
	if err != nil {
		return []error{}, errors.New(fmt.Sprintln("Error retrieving column list for: ", m.namespace, err))
	}

	// Make a lookup map for the column indices
	result.ColumnIdx = make(map[string]int, len(result.ColumnNames))
	for i, n := range result.ColumnNames {
		result.ColumnIdx[n] = i
	}

	result.ColumnData = make([]interface{}, len(result.ColumnNames))
	var scanArgs = make([]interface{}, len(result.ColumnNames))
	for i := range result.ColumnData {
		scanArgs[i] = &(result.ColumnData[i])
	}

	nonfatalErrors := []error{}

	for rows.Next() {
		err = rows.Scan(scanArgs...)
		if err != nil {
			return []error{}, errors.New(fmt.Sprintln("Error retrieving rows:", m.namespace, err))
		}

		n, e := m.rowFunc(m, &result, ch)
		if n != nil {
			nonfatalErrors = append(nonfatalErrors, n...)
		}
		if e != nil {
			return nonfatalErrors, e
		}
	}
	if err := rows.Err(); err != nil {
		log.Errorf("Failed scaning all rows due to scan failure: error was; %s", err)
		nonfatalErrors = append(nonfatalErrors, errors.New(fmt.Sprintf("Failed to consume all rows due to: %s", err)))
	}
	return nonfatalErrors, nil
}

func metricRowConverter(m *MetricMapNamespace, result *rowResult, ch chan<- prometheus.Metric) ([]error, error) {
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

func metricKVConverter(m *MetricMapNamespace, result *rowResult, ch chan<- prometheus.Metric) ([]error, error) {
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

func NewExporter(connectionString string, namespace string) *Exporter {

	db, err := getDB(connectionString)

	if err != nil {
		log.Fatal(err)
	}

	return &Exporter{
		metricMap: makeDescMap(namespace),
		namespace: namespace,
		db:        db,
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

// Convert database.sql to string for Prometheus labels. Null types are mapped to empty strings.
func dbToString(t interface{}) (string, bool) {
	switch v := t.(type) {
	case int64:
		return fmt.Sprintf("%v", v), true
	case float64:
		return fmt.Sprintf("%v", v), true
	case time.Time:
		return fmt.Sprintf("%v", v.Unix()), true
	case nil:
		return "", true
	case []byte:
		// Try and convert to string
		return string(v), true
	case string:
		return v, true
	default:
		return "", false
	}
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

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// We cannot know in advance what metrics the exporter will generate
	// from Postgres. So we use the poor man's describe method: Run a collect
	// and send the descriptors of all the collected metrics. The problem
	// here is that we need to connect to the Postgres DB. If it is currently
	// unavailable, the descriptors will be incomplete. Since this is a
	// stand-alone exporter and not used as a library within other code
	// implementing additional metrics, the worst that can happen is that we
	// don't detect inconsistent metrics created by this exporter
	// itself. Also, a change in the monitored Postgres instance may change the
	// exported metrics during the runtime of the exporter.

	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.up
	ch <- e.totalScrapes
	ch <- e.error
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
		log.Info("Ending scrape")
	}(time.Now())

	log.Info("Starting scrape")
	e.mutex.RLock()
	defer e.mutex.RUnlock()

	e.error.Set(0)
	e.totalScrapes.Inc()

	if err := e.db.Ping(); err != nil {
		log.Errorf("Backend is down, failed to connect: %s", err)
		e.error.Set(1)
		e.up.Set(0)
		return
	}
	log.Debug("Backend is up, proceeding with scrape")
	e.up.Set(1)

	for _, mapping := range e.metricMap {
		nonfatal, err := mapping.Query(ch, e.db)
		if len(nonfatal) > 0 {
			for _, suberr := range nonfatal {
				log.Errorln(suberr.Error())
			}
		}

		if err != nil {
			// this needs to be removed.
			log.Fatal(err)
		}
		e.error.Add(float64(len(nonfatal)))
	}
}

func makeDescMap(metricNamespace string) []*MetricMapNamespace {
	var metricMap []*MetricMapNamespace

	convert := func(namespace string, mappings map[string]ColumnMapping, converter RowConverter) *MetricMapNamespace {
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
		return &MetricMapNamespace{namespace: namespace, columnMappings: thisMap, labels: labels, rowFunc: converter}
	}

	for namespace, mappings := range metricRowMaps {
		metricMap = append(metricMap, convert(namespace, mappings, metricRowConverter))
	}
	for namespace, mappings := range metricKVMaps {
		metricMap = append(metricMap, convert(namespace, mappings, metricKVConverter))
	}
	return metricMap
}
