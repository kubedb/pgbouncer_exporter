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
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type columnUsage int

const (
	LABEL    columnUsage = iota // Use this column as a label
	COUNTER  columnUsage = iota // Use this column as a counter
	GAUGE    columnUsage = iota // Use this column as a gauge
	GAUGE_MS columnUsage = iota // Use this column for gauges that are microsecond data
)

type rowResult struct {
	ColumnNames []string
	ColumnIdx   map[string]int
	ColumnData  []interface{}
}

type RowConverter func(*MetricMapFromNamespace, *rowResult, chan<- prometheus.Metric) ([]error, error)

// Groups metric maps under a shared set of labels
type MetricMapFromNamespace struct {
	namespace      string
	columnMappings map[string]MetricMap // Column mappings in this namespace
	labels         []string
	rowFunc        RowConverter
}

// Stores the prometheus metric description which a given column will be mapped
// to by the collector
type MetricMap struct {
	vtype      prometheus.ValueType // Prometheus valuetype
	desc       *prometheus.Desc     // Prometheus descriptor
	multiplier float64              // This is a multiplier to apply pgbouncer values in converting to prometheus norms.
}

type ColumnMapping struct {
	usage          columnUsage `yaml:"usage"`
	promMetricName string      `yaml:"promMetricName"`
	description    string      `yaml:"description"`
}

// Exporter collects PgBouncer stats from the given server and exports
// them using the prometheus metrics package.
type Exporter struct {
	connectionString string
	namespace        string
	mutex            sync.RWMutex

	duration, up, error prometheus.Gauge
	totalScrapes        prometheus.Counter

	metricMap []*MetricMapFromNamespace

	db *sql.DB
}

var metricKVMaps = map[string]map[string]ColumnMapping{
	"config": {
		"listen_backlog":       {COUNTER, "", "Maximum number of backlogged listen connections before further connection attempts are dropped"},
		"max_client_conn":      {GAUGE, "", "Maximum number of client connections allowed"},
		"default_pool_size":    {GAUGE, "", "The default for how many server connections to allow per user/database pair"},
		"min_pool_size":        {GAUGE, "", "Mininum number of backends a pool will always retain."},
		"reserve_pool_size":    {GAUGE, "", "How many additional connections to allow to a pool once it's crossed it's maximum"},
		"reserve_pool_timeout": {GAUGE, "reserve_pool_timeout_seconds", "If a client has not been serviced in this many seconds, pgbouncer enables use of additional connections from reserve pool."},
		"max_db_connections":   {GAUGE, "", "Server level maximum connections enforced for a given db, irregardless of pool limits"},
		"max_user_connections": {GAUGE, "", "Maximum number of connections a user can open irregardless of pool limits"},
		"autodb_idle_timeout":  {GAUGE, "autodb_idle_timeout_seconds", "Unused pools created via '*' are reclaimed after this interval"},
		// server_reset_query should be enabled as a label for just this metric.
		"server_reset_query_always": {GAUGE, "", "Boolean indicating whether or not server_reset_query is enforced for all pooling modes, or just session"},
		// server_check_query should be enabled as a label for just this metric.
		"server_check_delay":        {GAUGE, "server_check_delay_seconds", "How long to keep released connections available for immediate re-use, without running sanity-check queries on it. If 0 then the query is ran always."},
		"query_timeout":             {GAUGE, "query_timeout_seconds", "Maximum time that a query can run for before being cancelled."},
		"query_wait_timeout":        {GAUGE, "query_wait_timeout_seconds", "Maximum time that a query can wait to be executed before being cancelled."},
		"client_idle_timeout":       {GAUGE, "client_idle_timeout_seconds", "Client connections idling longer than this many seconds are closed"},
		"client_login_timeout":      {GAUGE, "client_login_timeout_seconds", "Maximum time in seconds for a client to either login, or be disconnected"},
		"idle_transaction_timeout":  {GAUGE, "idle_transaction_timeout_seconds", "If client has been in 'idle in transaction' state longer than this amount in seconds, it will be disconnected."},
		"server_lifetime":           {GAUGE, "server_lifetime_seconds", "The pooler will close an unused server connection that has been connected longer than this many seconds"},
		"server_idle_timeout":       {GAUGE, "server_idle_timeout_seconds", "If a server connection has been idle more than this many seconds it will be dropped"},
		"server_connect_timeout":    {GAUGE, "server_connect_timeout_seconds", "Maximum time allowed for connecting and logging into a backend server"},
		"server_login_retry":        {GAUGE, "server_login_retry_seconds", "If connecting to a backend failed, this is the wait interval in seconds before retrying"},
		"server_round_robin":        {GAUGE, "", "Boolean; if 1, pgbouncer uses backends in a round robin fashion.  If 0, it uses LIFO to minimize connectivity to backends"},
		"suspend_timeout":           {GAUGE, "suspend_timeout_seconds", "Timeout for how long pgbouncer waits for buffer flushes before killing connections during pgbouncer admin SHUTDOWN and SUSPEND invocations."},
		"disable_pqexec":            {COUNTER, "", "Boolean; 1 means pgbouncer enforce Simple Query Protocol; 0 means it allows multiple queries in a single packet"},
		"dns_max_ttl":               {GAUGE, "", "Irregardless of DNS TTL, this is the TTL that pgbouncer enforces for dns lookups it does for backends"},
		"dns_nxdomain_ttl":          {GAUGE, "", "Irregardless of DNS TTL, this is the period enforced for negative DNS answers"},
		"dns_zone_check_period":     {GAUGE, "dns_zone_check_period_seconds", "Period to check if zone serial has changed."},
		"max_packet_size":           {GAUGE, "max_packet_size_bytes", "Maximum packet size for postgresql packets that pgbouncer will relay to backends"},
		"pkt_buf":                   {COUNTER, "pkt_buf_bytes", "Internal buffer size for packets.  See docs"},
		"sbuf_loopcnt":              {GAUGE, "sbuf_loopcnt", "How many results to process for a given connection's packet results before switching to others to ensure fairness.  See docs."},
		"tcp_defer_accept":          {GAUGE, "", "Configurable for TCP_DEFER_ACCEPT"},
		"tcp_socket_buffer":         {GAUGE, "tcp_socket_buffer_bytes", "Configurable for tcp socket buffering; 0 is kernel managed"},
		"tcpkeepalive":              {GAUGE, "", "Boolean; if 1, tcp keepalive is enabled w/ OS defaults.  If 0, disabled."},
		"tcp_keepcnt":               {GAUGE, "", "See TCP documentation for this field"},
		"tcp_keepidle":              {GAUGE, "", "See TCP documentation for this field"},
		"tcp_keepintvl":             {GAUGE, "", "See TCP documentation for this field"},
		"verbose":                   {GAUGE, "", "If log verbosity is increased.  Only relevant as a metric if log volume begins exceeding log consumption"},
		"stats_period":              {GAUGE, "stats_period_seconds", "Periodicity in seconds of pgbouncer recalculating internal stats."},
		"log_connections":           {GAUGE, "", "Whether connections are logged or not."},
		"log_disconnections":        {GAUGE, "", "Whether connection disconnects are logged."},
		"log_pooler_errors":         {GAUGE, "", "Whether pooler errors are logged or not"},
		"application_name_add_host": {GAUGE, "", "Whether pgbouncer add the client host address and port to the application name setting set on connection start or not"},
	},
}

var metricRowMaps = map[string]map[string]ColumnMapping{
	"databases": {
		"name":                {LABEL, "", ""},
		"host":                {LABEL, "", ""},
		"port":                {LABEL, "", ""},
		"database":            {LABEL, "", ""},
		"force_user":          {LABEL, "", ""},
		"pool_size":           {GAUGE, "", "Maximum number of connection per pool for backend connections"},
		"reserve_pool":        {GAUGE, "reserve_pool_size", "Number of extra connections by which the pool_size can be exceeded temporarily"},
		"pool_mode":           {LABEL, "", "Nature of connection pooling"},
		"max_connections":     {GAUGE, "", "Maximum number of client connections allowed"},
		"current_connections": {GAUGE, "", "Current number of client connections"},
		"paused":              {GAUGE, "", "Boolean indicating whether a pgbouncer PAUSE is currently active for this database"},
		"disabled":            {GAUGE, "", "Boolean indicating whether a pgbouncer DISABLE is currently active for this database"},
	},
	"lists": {
		"databases":     {GAUGE, "", "Count of databases"},
		"users":         {GAUGE, "", "Count of users"},
		"pools":         {GAUGE, "", "Count of pools"},
		"free_clients":  {GAUGE, "", "Count of free clients"},
		"used_clients":  {GAUGE, "", "Count of used clients"},
		"login_clients": {GAUGE, "", "Count of clients in login state"},
		"free_servers":  {GAUGE, "", "Count of free servers"},
		"used_servers":  {GAUGE, "", "Count of used servers"},
	},
	"pools": {
		"database":   {LABEL, "", ""},
		"user":       {LABEL, "", ""},
		"cl_active":  {GAUGE, "", "Client connections linked to server connection and able to process queries, shown as connection"},
		"cl_waiting": {GAUGE, "", "Client connections waiting on a server connection, shown as connection"},
		"sv_active":  {GAUGE, "", "Server connections linked to a client connection, shown as connection"},
		"sv_idle":    {GAUGE, "", "Server connections idle and ready for a client query, shown as connection"},
		"sv_used":    {GAUGE, "", "Server connections idle more than server_check_delay, needing server_check_query, shown as connection"},
		"sv_tested":  {GAUGE, "", "Server connections currently running either server_reset_query or server_check_query, shown as connection"},
		"sv_login":   {GAUGE, "", "Server connections currently in the process of logging in, shown as connection"},
		"maxwait":    {GAUGE, "maxwait_seconds", "Age of oldest unserved client connection, shown as second"},
		"pool_mode":  {LABEL, "", ""},
	},
	"stats": {
		"database":                  {LABEL, "", ""},
		"avg_query_count":           {GAUGE, "avg_queries_per_second", "Average queries per second in last stat period"},
		"avg_query":                 {GAUGE_MS, "avg_query_duration_microseconds", "The average query duration, shown as microsecond"},
		"avg_query_time":            {GAUGE_MS, "avg_query_time_microseconds", "Average query time in microseconds"},
		"avg_recv":                  {GAUGE, "avg_data_recv_bytes_per_second", "Average received (from clients) bytes per second"},
		"avg_req":                   {GAUGE, "", "The average number of requests per second in last stat period, shown as request/second"},
		"avg_sent":                  {GAUGE, "", "Average sent (to clients) bytes per second"},
		"avg_wait_time":             {GAUGE_MS, "avg_wait_time_microseconds", "Time spent by clients waiting for a server in microseconds (average per second)"},
		"avg_xact_count":            {GAUGE, "", "Average transactions per second in last stat period"},
		"avg_xact_time":             {GAUGE_MS, "avg_xact_time_microseconds", "Average transaction duration in microseconds"},
		"bytes_received_per_second": {GAUGE, "bytes_received_per_second_total", "The total network traffic received, shown as byte/second"},
		"bytes_sent_per_second":     {GAUGE, "bytes_sent_per_second_total", "The total network traffic sent, shown as byte/second"},
		"total_query_count":         {GAUGE, "query_count_total", "Total number of SQL queries pooled"},
		"total_query_time":          {GAUGE_MS, "query_time_microseconds_total", "Total number of microseconds spent by pgbouncer when actively connected to PostgreSQL, executing queries"},
		"total_received":            {GAUGE, "received_bytes_total", "Total volume in bytes of network traffic received by pgbouncer, shown as bytes"},
		"total_requests":            {GAUGE, "requests_total", "Total number of SQL requests pooled by pgbouncer, shown as requests"},
		"total_sent":                {GAUGE, "sent_bytes_total", "Total volume in bytes of network traffic sent by pgbouncer, shown as bytes"},
		"total_wait_time":           {GAUGE_MS, "wait_time_microseconds_total", "Time spent by clients waiting for a server in microseconds"},
		"total_xact_count":          {GAUGE, "xact_count_total", "Total number of SQL transactions pooled"},
		"total_xact_time":           {GAUGE_MS, "xact_time_microseconds_total", "Total number of microseconds spent by pgbouncer when connected to PostgreSQL in a transaction, either idle in transaction or executing queries"},
	},
}

//TODO: delete commented out part when these changes are confirmed
/*
var metricKVMaps = map[string]map[string]ColumnMapping{
	"config": {
		"listen_backlog":       {COUNTER, "listen_backlog", "Maximum number of backlogged listen connections before further connection attempts are dropped"},
		"max_client_conn":      {GAUGE, "max_client_conn", "Maximum number of client connections allowed"},
		"default_pool_size":    {GAUGE, "default_pool_size","The default for how many server connections to allow per user/database pair"},
		"min_pool_size":        {GAUGE, "min_pool_size", "Mininum number of backends a pool will always retain."},
		"reserve_pool_size":    {GAUGE, "reserve_pool_size", "How many additional connections to allow to a pool once it's crossed it's maximum"},
		"reserve_pool_timeout": {GAUGE, "reserve_pool_timeout_seconds", "If a client has not been serviced in this many seconds, pgbouncer enables use of additional connections from reserve pool."},
		"max_db_connections":   {GAUGE, "max_db_connections", "Server level maximum connections enforced for a given db, irregardless of pool limits"},
		"max_user_connections": {GAUGE, "max_user_connections", "Maximum number of connections a user can open irregardless of pool limits"},
		"autodb_idle_timeout":  {GAUGE, "autodb_idle_timeout_seconds", "Unused pools created via '*' are reclaimed after this interval"},
		// server_reset_query should be enabled as a label for just this metric.
		"server_reset_query_always": {GAUGE, "server_reset_query_always", "Boolean indicating whether or not server_reset_query is enforced for all pooling modes, or just session"},
		// server_check_query should be enabled as a label for just this metric.
		"server_check_delay":        {GAUGE, "server_check_delay_seconds", "How long to keep released connections available for immediate re-use, without running sanity-check queries on it. If 0 then the query is ran always."},
		"query_timeout":             {GAUGE, "query_timeout_seconds", "Maximum time that a query can run for before being cancelled."},
		"query_wait_timeout":        {GAUGE, "query_wait_timeout_seconds", "Maximum time that a query can wait to be executed before being cancelled."},
		"client_idle_timeout":       {GAUGE, "client_idle_timeout_seconds", "Client connections idling longer than this many seconds are closed"},
		"client_login_timeout":      {GAUGE, "client_login_timeout_seconds", "Maximum time in seconds for a client to either login, or be disconnected"},
		"idle_transaction_timeout":  {GAUGE, "idle_transaction_timeout_seconds", "If client has been in 'idle in transaction' state longer than this amount in seconds, it will be disconnected."},
		"server_lifetime":           {GAUGE, "server_lifetime_seconds", "The pooler will close an unused server connection that has been connected longer than this many seconds"},
		"server_idle_timeout":       {GAUGE, "server_idle_timeout_seconds", "If a server connection has been idle more than this many seconds it will be dropped"},
		"server_connect_timeout":    {GAUGE, "server_connect_timeout_seconds", "Maximum time allowed for connecting and logging into a backend server"},
		"server_login_retry":        {GAUGE, "server_login_retry_seconds", "If connecting to a backend failed, this is the wait interval in seconds before retrying"},
		"server_round_robin":        {GAUGE, "server_round_robin", "Boolean; if 1, pgbouncer uses backends in a round robin fashion.  If 0, it uses LIFO to minimize connectivity to backends"},
		"suspend_timeout":           {GAUGE, "suspend_timeout_seconds", "Timeout for how long pgbouncer waits for buffer flushes before killing connections during pgbouncer admin SHUTDOWN and SUSPEND invocations."},
		"disable_pqexec":            {COUNTER, "disable_pqexec","Boolean; 1 means pgbouncer enforce Simple Query Protocol; 0 means it allows multiple queries in a single packet"},
		"dns_max_ttl":               {GAUGE, "dns_max_ttl", "Irregardless of DNS TTL, this is the TTL that pgbouncer enforces for dns lookups it does for backends"},
		"dns_nxdomain_ttl":          {GAUGE, "dns_nxdomain_ttl", "Irregardless of DNS TTL, this is the period enforced for negative DNS answers"},
		"dns_zone_check_period":     {GAUGE, "dns_zone_check_period_seconds", "Period to check if zone serial has changed."},
		"max_packet_size":           {GAUGE, "max_packet_size_bytes","Maximum packet size for postgresql packets that pgbouncer will relay to backends"},
		"pkt_buf":                   {COUNTER, "pkt_buf_bytes", "Internal buffer size for packets.  See docs"},
		"sbuf_loopcnt":              {GAUGE, "sbuf_loopcnt", "How many results to process for a given connection's packet results before switching to others to ensure fairness.  See docs."},
		"tcp_defer_accept":          {GAUGE, "tcp_defer_accept","Configurable for TCP_DEFER_ACCEPT"},
		"tcp_socket_buffer":         {GAUGE, "tcp_socket_buffer_bytes","Configurable for tcp socket buffering; 0 is kernel managed"},
		"tcpkeepalive":              {GAUGE, "tcpkeepalive", "Boolean; if 1, tcp keepalive is enabled w/ OS defaults.  If 0, disabled."},
		"tcp_keepcnt":               {GAUGE, "tcp_keepcnt", "See TCP documentation for this field"},
		"tcp_keepidle":              {GAUGE, "tcp_keepidle", "See TCP documentation for this field"},
		"tcp_keepintvl":             {GAUGE, "tcp_keepintvl", "See TCP documentation for this field"},
		"verbose":                   {GAUGE, "verbose", "If log verbosity is increased.  Only relevant as a metric if log volume begins exceeding log consumption"},
		"stats_period":              {GAUGE, "stats_period_seconds", "Periodicity in seconds of pgbouncer recalculating internal stats."},
		"log_connections":           {GAUGE, "log_connections", "Whether connections are logged or not."},
		"log_disconnections":        {GAUGE, "log_disconnections", "Whether connection disconnects are logged."},
		"log_pooler_errors":         {GAUGE, "log_pooler_errors", "Whether pooler errors are logged or not"},
		"application_name_add_host": {GAUGE, "application_name_add_host", "Whether pgbouncer add the client host address and port to the application name setting set on connection start or not"},
	},
}

var metricRowMaps = map[string]map[string]ColumnMapping{
	"databases": {
		"name":                {LABEL, "name", ""},
		"host":                {LABEL, "host", ""},
		"port":                {LABEL, "database", ""},
		"database":            {LABEL, "database", ""},
		"force_user":          {LABEL, "force_user", ""},
		"pool_size":           {GAUGE, "pool_size", "Maximum number of pool backend connections"},
		"reserve_pool":        {GAUGE, "reserve_pool", "Maximum amount that the pool size can be exceeded temporarily"},
		"pool_mode":           {LABEL, "pool_mode", ""},
		"max_connections":     {GAUGE, "max_connections", "Maximum number of client connections allowed"},
		"current_connections": {GAUGE, "current_connections", "Current number of client connections"},
		"paused":              {GAUGE, "paused", "Boolean indicating whether a pgbouncer PAUSE is currently active for this database"},
		"disabled":            {GAUGE, "disabled", "Boolean indicating whether a pgbouncer DISABLE is currently active for this database"},
	},
	"lists": {
		"databases":     {GAUGE, "databases", "Count of databases"},
		"users":         {GAUGE, "users", "Count of users"},
		"pools":         {GAUGE, "pools", "Count of pools"},
		"free_clients":  {GAUGE, "free_clients", "Count of free clients"},
		"used_clients":  {GAUGE, "used_clients", "Count of used clients"},
		"login_clients": {GAUGE, "login_clients", "Count of clients in login state"},
		"free_servers":  {GAUGE, "free_servers", "Count of free servers"},
		"used_servers":  {GAUGE, "used_servers", "Count of used servers"},
	},
	"pools": {
		"database":   {LABEL, "database", ""},
		"user":       {LABEL, "user", ""},
		"cl_active":  {GAUGE, "cl_active", "Client connections linked to server connection and able to process queries, shown as connection"},
		"cl_waiting": {GAUGE, "cl_waiting", "Client connections waiting on a server connection, shown as connection"},
		"sv_active":  {GAUGE, "sv_active", "Server connections linked to a client connection, shown as connection"},
		"sv_idle":    {GAUGE, "sv_idle", "Server connections idle and ready for a client query, shown as connection"},
		"sv_used":    {GAUGE, "sv_used", "Server connections idle more than server_check_delay, needing server_check_query, shown as connection"},
		"sv_tested":  {GAUGE, "sv_tested", "Server connections currently running either server_reset_query or server_check_query, shown as connection"},
		"sv_login":   {GAUGE, "sv_login", "Server connections currently in the process of logging in, shown as connection"},
		"maxwait":    {GAUGE, "maxwait", "Age of oldest unserved client connection, shown as second"},
		"pool_mode":  {LABEL, "pool_mode", ""},
	},
	"stats": {
		"database":                  {LABEL, "database", ""},
		"avg_query_count":           {GAUGE, "avg_queries_per_second", "Average queries per second in last stat period"},
		"avg_query":                 {GAUGE_MS, "avg_query_duration_microseconds", "The average query duration, shown as microsecond"},
		"avg_query_time":            {GAUGE_MS, "avg_query_time_microseconds", "Average query duration in microseconds"},
		"avg_recv":                  {GAUGE, "avg_data_recv_bytes_per_second", "Average received (from clients) bytes per second"},
		"avg_req":                   {GAUGE, "avg_req", "The average number of requests per second in last stat period, shown as request/second"},
		"avg_sent":                  {GAUGE, "avg_sent", "Average sent (to clients) bytes per second"},
		"avg_wait_time":             {GAUGE_MS, "avg_wait_time_microseconds", "Time spent by clients waiting for a server in microseconds (average per second)"},
		"avg_xact_count":            {GAUGE, "avg_xact_count", "Average transactions per second in last stat period"},
		"avg_xact_time":             {GAUGE_MS, "avg_xact_time_microseconds", "Average transaction duration in microseconds"},
		"bytes_received_per_second": {GAUGE, "bytes_received_per_second_total", "The total network traffic received, shown as byte/second"},
		"bytes_sent_per_second":     {GAUGE, "bytes_sent_per_second_total", "The total network traffic sent, shown as byte/second"},
		"total_query_count":         {GAUGE, "query_count_total", "Total number of SQL queries pooled"},
		"total_query_time":          {GAUGE_MS, "query_time_microseconds_total", "Total number of microseconds spent by pgbouncer when actively connected to PostgreSQL, executing queries"},
		"total_received":            {GAUGE, "received_bytes_total", "Total volume in bytes of network traffic received by pgbouncer, shown as bytes"},
		"total_requests":            {GAUGE, "requests_total", "Total number of SQL requests pooled by pgbouncer, shown as requests"},
		"total_sent":                {GAUGE, "sent_bytes_total", "Total volume in bytes of network traffic sent by pgbouncer, shown as bytes"},
		"total_wait_time":           {GAUGE_MS, "wait_time_microseconds_total", "Time spent by clients waiting for a server in microseconds"},
		"total_xact_count":          {GAUGE, "xact_count_total", "Total number of SQL transactions pooled"},
		"total_xact_time":           {GAUGE_MS, "xact_time_microseconds_total", "Total number of microseconds spent by pgbouncer when connected to PostgreSQL in a transaction, either idle in transaction or executing queries"},
	},
}


*/
