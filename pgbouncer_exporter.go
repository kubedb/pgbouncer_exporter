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
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
)

const (
	namespace = "pgbouncer"
	indexHTML = `
	<html>
		<head>
			<title>PgBouncer Exporter</title>
		</head>
		<body>
			<h1>PgBouncer Exporter</h1>
			<p>
			<a href='%s'>Metrics</a>
			</p>
		</body>
	</html>`
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	var (
		showVersion             = flag.Bool("version", false, "Print version information.")
		listenAddress           = flag.String("web.listen-address", ":9127", "Address on which to expose metrics and web interface.")
		connectionStringPointer = flag.String("pgBouncer.connectionString", "postgres://postgres:@localhost:6543/pgbouncer?sslmode=disable",
			"Connection string for accessing pgBouncer. Can also be set using environment variable DATA_SOURCE_NAME")
		//"postgres://YourUserName:YourPassword@YourHost:5432/pgbouncer";
		metricsPath = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	)
	flag.Parse()

	if *showVersion {
		if _, err := fmt.Fprintln(os.Stdout, version.Print("pgbouncer_exporter")); err != nil {
			log.Infoln("Version err : ", err)
		}
		os.Exit(0)
	}

	connectionString := getEnv("DATA_SOURCE_NAME", *connectionStringPointer)
	exporter := NewExporter(connectionString, namespace)
	prometheus.MustRegister(exporter)

	log.Infoln("Starting pgbouncer exporter version: ", version.Info())

	http.Handle(*metricsPath, promhttp.Handler())

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		//Handle func for root. Contains a link to exposed metrics
		if _, err := w.Write([]byte(fmt.Sprintf(indexHTML, *metricsPath))); err != nil {
			log.Infoln("Write err : ", err)
		}
	})

	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
