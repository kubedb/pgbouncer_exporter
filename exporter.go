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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

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

	rows, err := e.db.Query("SHOW STATS")
	if err != nil {
		log.Errorf("error pinging pgbouncer: %q", err)
		e.error.Set(1)
		e.up.Set(0)
		return
	}
	_ = rows.Close()
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

// the scrape fails, and a slice of errors if they were non-fatal.
func (m *MetricMapFromNamespace) Query(ch chan<- prometheus.Metric, db *sql.DB) ([]error, error) {
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

	var nonfatalErrors []error

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
		log.Errorf("Failed scanning all rows due to scan failure: error was; %s", err)
		nonfatalErrors = append(nonfatalErrors, fmt.Errorf("failed to consume all rows due to: %s", err))
	}
	return nonfatalErrors, nil
}
