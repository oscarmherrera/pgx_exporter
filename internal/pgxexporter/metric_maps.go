package pgxexporter

import (
	"fmt"
	"github.com/blang/semver"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"math"
	"time"
)

// MetricMapNamespace groups metric maps under a shared set of labels.
type MetricMapNamespace struct {
	labels         []string             // Label names for this namespace
	columnMappings map[string]MetricMap // Column mappings in this namespace
}

func (mmn *MetricMapNamespace) GetColumnMapping(mapName string) *MetricMap {
	metricMap := mmn.columnMappings[mapName]
	return &metricMap
}

func (mmn *MetricMapNamespace) GetMetricMap(metricMapNamespaces *map[string]MetricMapNamespace, namespace string, mapName string) *MetricMap {
	metricMapNamespaceList := *metricMapNamespaces

	metricMap := metricMapNamespaceList[namespace].columnMappings[mapName]
	return &metricMap
}

func GetNamespace(metricMapNamespaces *map[string]MetricMapNamespace, mapName string) *MetricMapNamespace {
	var maps = *metricMapNamespaces
	namespace := maps[mapName]

	return &namespace
}

// MetricMap stores the prometheus metric description which a given column will
// be mapped to by the collector
type MetricMap struct {
	discard    bool                              // Should metric be discarded during mapping?
	vtype      prometheus.ValueType              // Prometheus valuetype
	desc       *prometheus.Desc                  // Prometheus descriptor
	conversion func(interface{}) (float64, bool) // Conversion function to turn PG result into float64
}

func (mm *MetricMap) GetDiscard() bool {
	return mm.discard
}

var builtinMetricMaps = map[string]map[string]ColumnMapping{
	"pg_stat_bgwriter": {
		"checkpoints_timed":     {COUNTER, "Number of scheduled checkpoints that have been performed", nil, nil},
		"checkpoints_req":       {COUNTER, "Number of requested checkpoints that have been performed", nil, nil},
		"checkpoint_write_time": {COUNTER, "Total amount of time that has been spent in the portion of checkpoint processing where files are written to disk, in milliseconds", nil, nil},
		"checkpoint_sync_time":  {COUNTER, "Total amount of time that has been spent in the portion of checkpoint processing where files are synchronized to disk, in milliseconds", nil, nil},
		"buffers_checkpoint":    {COUNTER, "Number of buffers written during checkpoints", nil, nil},
		"buffers_clean":         {COUNTER, "Number of buffers written by the background writer", nil, nil},
		"maxwritten_clean":      {COUNTER, "Number of times the background writer stopped a cleaning scan because it had written too many buffers", nil, nil},
		"buffers_backend":       {COUNTER, "Number of buffers written directly by a backend", nil, nil},
		"buffers_backend_fsync": {COUNTER, "Number of times a backend had to execute its own fsync call (normally the background writer handles those even when the backend does its own write)", nil, nil},
		"buffers_alloc":         {COUNTER, "Number of buffers allocated", nil, nil},
		"stats_reset":           {COUNTER, "Time at which these statistics were last reset", nil, nil},
	},
	"pg_stat_database": {
		"datid":          {LABEL, "OID of a database", nil, nil},
		"datname":        {LABEL, "Name of this database", nil, nil},
		"numbackends":    {GAUGE, "Number of backends currently connected to this database. This is the only column in this view that returns a value reflecting current state; all other columns return the accumulated values since the last reset.", nil, nil},
		"xact_commit":    {COUNTER, "Number of transactions in this database that have been committed", nil, nil},
		"xact_rollback":  {COUNTER, "Number of transactions in this database that have been rolled back", nil, nil},
		"blks_read":      {COUNTER, "Number of disk blocks read in this database", nil, nil},
		"blks_hit":       {COUNTER, "Number of times disk blocks were found already in the buffer cache, so that a read was not necessary (this only includes hits in the PostgreSQL buffer cache, not the operating system's file system cache)", nil, nil},
		"tup_returned":   {COUNTER, "Number of rows returned by queries in this database", nil, nil},
		"tup_fetched":    {COUNTER, "Number of rows fetched by queries in this database", nil, nil},
		"tup_inserted":   {COUNTER, "Number of rows inserted by queries in this database", nil, nil},
		"tup_updated":    {COUNTER, "Number of rows updated by queries in this database", nil, nil},
		"tup_deleted":    {COUNTER, "Number of rows deleted by queries in this database", nil, nil},
		"conflicts":      {COUNTER, "Number of queries canceled due to conflicts with recovery in this database. (Conflicts occur only on standby servers; see pg_stat_database_conflicts for details.)", nil, nil},
		"temp_files":     {COUNTER, "Number of temporary files created by queries in this database. All temporary files are counted, regardless of why the temporary file was created (e.g., sorting or hashing), and regardless of the log_temp_files setting.", nil, nil},
		"temp_bytes":     {COUNTER, "Total amount of data written to temporary files by queries in this database. All temporary files are counted, regardless of why the temporary file was created, and regardless of the log_temp_files setting.", nil, nil},
		"deadlocks":      {COUNTER, "Number of deadlocks detected in this database", nil, nil},
		"blk_read_time":  {COUNTER, "Time spent reading data file blocks by backends in this database, in milliseconds", nil, nil},
		"blk_write_time": {COUNTER, "Time spent writing data file blocks by backends in this database, in milliseconds", nil, nil},
		"stats_reset":    {COUNTER, "Time at which these statistics were last reset", nil, nil},
	},
	"pg_stat_database_conflicts": {
		"datid":            {LABEL, "OID of a database", nil, nil},
		"datname":          {LABEL, "Name of this database", nil, nil},
		"confl_tablespace": {COUNTER, "Number of queries in this database that have been canceled due to dropped tablespaces", nil, nil},
		"confl_lock":       {COUNTER, "Number of queries in this database that have been canceled due to lock timeouts", nil, nil},
		"confl_snapshot":   {COUNTER, "Number of queries in this database that have been canceled due to old snapshots", nil, nil},
		"confl_bufferpin":  {COUNTER, "Number of queries in this database that have been canceled due to pinned buffers", nil, nil},
		"confl_deadlock":   {COUNTER, "Number of queries in this database that have been canceled due to deadlocks", nil, nil},
	},
	"pg_locks": {
		"datname": {LABEL, "Name of this database", nil, nil},
		"mode":    {LABEL, "Type of Lock", nil, nil},
		"count":   {GAUGE, "Number of locks", nil, nil},
	},
	"pg_stat_replication": {
		"procpid":          {DISCARD, "Process ID of a WAL sender process", nil, semver.MustParseRange("<9.2.0")},
		"pid":              {DISCARD, "Process ID of a WAL sender process", nil, semver.MustParseRange(">=9.2.0")},
		"usesysid":         {DISCARD, "OID of the user logged into this WAL sender process", nil, nil},
		"usename":          {DISCARD, "Name of the user logged into this WAL sender process", nil, nil},
		"application_name": {LABEL, "Name of the application that is connected to this WAL sender", nil, nil},
		"client_addr":      {LABEL, "IP address of the client connected to this WAL sender. If this field is null, it indicates that the client is connected via a Unix socket on the server machine.", nil, nil},
		"client_hostname":  {DISCARD, "Host name of the connected client, as reported by a reverse DNS lookup of client_addr. This field will only be non-null for IP connections, and only when log_hostname is enabled.", nil, nil},
		"client_port":      {DISCARD, "TCP port number that the client is using for communication with this WAL sender, or -1 if a Unix socket is used", nil, nil},
		"backend_start": {DISCARD, "with time zone	Time when this process was started, i.e., when the client connected to this WAL sender", nil, nil},
		"backend_xmin":             {DISCARD, "The current backend's xmin horizon.", nil, nil},
		"state":                    {LABEL, "Current WAL sender state", nil, nil},
		"sent_location":            {DISCARD, "Last transaction log position sent on this connection", nil, semver.MustParseRange("<10.0.0")},
		"write_location":           {DISCARD, "Last transaction log position written to disk by this standby server", nil, semver.MustParseRange("<10.0.0")},
		"flush_location":           {DISCARD, "Last transaction log position flushed to disk by this standby server", nil, semver.MustParseRange("<10.0.0")},
		"replay_location":          {DISCARD, "Last transaction log position replayed into the database on this standby server", nil, semver.MustParseRange("<10.0.0")},
		"sent_lsn":                 {DISCARD, "Last transaction log position sent on this connection", nil, semver.MustParseRange(">=10.0.0")},
		"write_lsn":                {DISCARD, "Last transaction log position written to disk by this standby server", nil, semver.MustParseRange(">=10.0.0")},
		"flush_lsn":                {DISCARD, "Last transaction log position flushed to disk by this standby server", nil, semver.MustParseRange(">=10.0.0")},
		"replay_lsn":               {DISCARD, "Last transaction log position replayed into the database on this standby server", nil, semver.MustParseRange(">=10.0.0")},
		"sync_priority":            {DISCARD, "Priority of this standby server for being chosen as the synchronous standby", nil, nil},
		"sync_state":               {DISCARD, "Synchronous state of this standby server", nil, nil},
		"slot_name":                {LABEL, "A unique, cluster-wide identifier for the replication slot", nil, semver.MustParseRange(">=9.2.0")},
		"plugin":                   {DISCARD, "The base name of the shared object containing the output plugin this logical slot is using, or null for physical slots", nil, nil},
		"slot_type":                {DISCARD, "The slot type - physical or logical", nil, nil},
		"datoid":                   {DISCARD, "The OID of the database this slot is associated with, or null. Only logical slots have an associated database", nil, nil},
		"database":                 {DISCARD, "The name of the database this slot is associated with, or null. Only logical slots have an associated database", nil, nil},
		"active":                   {DISCARD, "True if this slot is currently actively being used", nil, nil},
		"active_pid":               {DISCARD, "Process ID of a WAL sender process", nil, nil},
		"xmin":                     {DISCARD, "The oldest transaction that this slot needs the database to retain. VACUUM cannot remove tuples deleted by any later transaction", nil, nil},
		"catalog_xmin":             {DISCARD, "The oldest transaction affecting the system catalogs that this slot needs the database to retain. VACUUM cannot remove catalog tuples deleted by any later transaction", nil, nil},
		"restart_lsn":              {DISCARD, "The address (LSN) of oldest WAL which still might be required by the consumer of this slot and thus won't be automatically removed during checkpoints", nil, nil},
		"pg_current_xlog_location": {DISCARD, "pg_current_xlog_location", nil, nil},
		"pg_current_wal_lsn":       {DISCARD, "pg_current_xlog_location", nil, semver.MustParseRange(">=10.0.0")},
		"pg_xlog_location_diff":    {GAUGE, "Lag in bytes between master and slave", nil, semver.MustParseRange(">=9.2.0 <10.0.0")},
		"pg_wal_lsn_diff":          {GAUGE, "Lag in bytes between master and slave", nil, semver.MustParseRange(">=10.0.0")},
		"confirmed_flush_lsn":      {DISCARD, "LSN position a consumer of a slot has confirmed flushing the data received", nil, nil},
		"write_lag":                {DISCARD, "Time elapsed between flushing recent WAL locally and receiving notification that this standby server has written it (but not yet flushed it or applied it). This can be used to gauge the delay that synchronous_commit level remote_write incurred while committing if this server was configured as a synchronous standby.", nil, semver.MustParseRange(">=10.0.0")},
		"flush_lag":                {DISCARD, "Time elapsed between flushing recent WAL locally and receiving notification that this standby server has written and flushed it (but not yet applied it). This can be used to gauge the delay that synchronous_commit level remote_flush incurred while committing if this server was configured as a synchronous standby.", nil, semver.MustParseRange(">=10.0.0")},
		"replay_lag":               {DISCARD, "Time elapsed between flushing recent WAL locally and receiving notification that this standby server has written, flushed and applied it. This can be used to gauge the delay that synchronous_commit level remote_apply incurred while committing if this server was configured as a synchronous standby.", nil, semver.MustParseRange(">=10.0.0")},
	},
	"pg_stat_activity": {
		"datname":         {LABEL, "Name of this database", nil, nil},
		"state":           {LABEL, "connection state", nil, semver.MustParseRange(">=9.2.0")},
		"count":           {GAUGE, "number of connections in this state", nil, nil},
		"max_tx_duration": {GAUGE, "max duration in seconds any active transaction has been running", nil, nil},
	},
}

// MakeDescMap Abstracting the private function for now
// This turns the MetricMap column mapping into a prometheus descriptor mapping.
func MakeDescMap(pgVersion semver.Version, serverLabels prometheus.Labels, metricMaps map[string]map[string]ColumnMapping) map[string]MetricMapNamespace {
	return makeDescMap(pgVersion, serverLabels, metricMaps)
}

// Turn the MetricMap column mapping into a prometheus descriptor mapping.
func makeDescMap(pgVersion semver.Version, serverLabels prometheus.Labels, metricMaps map[string]map[string]ColumnMapping) map[string]MetricMapNamespace {
	var metricMap = make(map[string]MetricMapNamespace)

	for namespace, mappings := range metricMaps {
		thisMap := make(map[string]MetricMap)

		// Get the constant labels
		var variableLabels []string
		for columnName, columnMapping := range mappings {
			if columnMapping.usage == LABEL {
				variableLabels = append(variableLabels, columnName)
			}
		}

		for columnName, columnMapping := range mappings {
			// Check column version compatibility for the current map
			// Force to discard if not compatible.
			if columnMapping.supportedVersions != nil {
				if !columnMapping.supportedVersions(pgVersion) {
					// It's very useful to be able to see what columns are being
					// rejected.
					log.Debugln(columnName, "is being forced to discard due to version incompatibility.")
					thisMap[columnName] = MetricMap{
						discard: true,
						conversion: func(_ interface{}) (float64, bool) {
							return math.NaN(), true
						},
					}
					continue
				}
			}

			// Determine how to convert the column based on its usage.
			// nolint: dupl
			switch columnMapping.usage {
			case DISCARD, LABEL:
				thisMap[columnName] = MetricMap{
					discard: true,
					conversion: func(_ interface{}) (float64, bool) {
						return math.NaN(), true
					},
				}
			case COUNTER:
				thisMap[columnName] = MetricMap{
					vtype: prometheus.CounterValue,
					desc:  prometheus.NewDesc(fmt.Sprintf("%s_%s", namespace, columnName), columnMapping.description, variableLabels, serverLabels),
					conversion: func(in interface{}) (float64, bool) {
						return DBToFloat64(in)
					},
				}
			case GAUGE:
				thisMap[columnName] = MetricMap{
					vtype: prometheus.GaugeValue,
					desc:  prometheus.NewDesc(fmt.Sprintf("%s_%s", namespace, columnName), columnMapping.description, variableLabels, serverLabels),
					conversion: func(in interface{}) (float64, bool) {
						return DBToFloat64(in)
					},
				}
			case MAPPEDMETRIC:
				thisMap[columnName] = MetricMap{
					vtype: prometheus.GaugeValue,
					desc:  prometheus.NewDesc(fmt.Sprintf("%s_%s", namespace, columnName), columnMapping.description, variableLabels, serverLabels),
					conversion: func(in interface{}) (float64, bool) {
						text, ok := in.(string)
						if !ok {
							return math.NaN(), false
						}

						val, ok := columnMapping.mapping[text]
						if !ok {
							return math.NaN(), false
						}
						return val, true
					},
				}
			case DURATION:
				thisMap[columnName] = MetricMap{
					vtype: prometheus.GaugeValue,
					desc:  prometheus.NewDesc(fmt.Sprintf("%s_%s_milliseconds", namespace, columnName), columnMapping.description, variableLabels, serverLabels),
					conversion: func(in interface{}) (float64, bool) {
						var durationString string
						switch t := in.(type) {
						case []byte:
							durationString = string(t)
						case string:
							durationString = t
						default:
							log.Errorln("DURATION conversion metric was not a string")
							return math.NaN(), false
						}

						if durationString == "-1" {
							return math.NaN(), false
						}

						d, err := time.ParseDuration(durationString)
						if err != nil {
							log.Errorln("Failed converting result to metric:", columnName, in, err)
							return math.NaN(), false
						}
						return float64(d / time.Millisecond), true
					},
				}
			}
		}

		metricMap[namespace] = MetricMapNamespace{variableLabels, thisMap}
	}

	return metricMap
}

func newDesc(subsystem, name, help string, labels prometheus.Labels) *prometheus.Desc {
	return prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, name),
		help, nil, labels,
	)
}
