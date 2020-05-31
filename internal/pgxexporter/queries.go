package pgxexporter

import (
	"context"
	"errors"
	"fmt"
	"github.com/blang/semver"
	"github.com/jackc/pgproto3/v2"
	pgx "github.com/jackc/pgx/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"gopkg.in/yaml.v2"
)

// OverrideQuery 's are run in-place of simple namespace look ups, and provide
// advanced functionality. But they have a tendency to postgres version specific.
// There aren't too many versions, so we simply store customized versions using
// the semver matching we do for columns.
type OverrideQuery struct {
	versionRange semver.Range
	query        string
}

// Overriding queries for namespaces above.
// TODO: validate this is a closed set in tests, and there are no overlaps
var queryOverrides = map[string][]OverrideQuery{
	"pg_locks": {
		{
			semver.MustParseRange(">0.0.0"),
			`SELECT pg_database.datname,tmp.mode,COALESCE(count,0) as count
			FROM
				(
				  VALUES ('accesssharelock'),
				         ('rowsharelock'),
				         ('rowexclusivelock'),
				         ('shareupdateexclusivelock'),
				         ('sharelock'),
				         ('sharerowexclusivelock'),
				         ('exclusivelock'),
				         ('accessexclusivelock')
				) AS tmp(mode) CROSS JOIN pg_database
			LEFT JOIN
			  (SELECT database, lower(mode) AS mode,count(*) AS count
			  FROM pg_locks WHERE database IS NOT NULL
			  GROUP BY database, lower(mode)
			) AS tmp2
			ON tmp.mode=tmp2.mode and pg_database.oid = tmp2.database ORDER BY 1`,
		},
	},

	"pg_stat_replication": {
		{
			semver.MustParseRange(">=10.0.0"),
			`
			SELECT *,
				(case pg_is_in_recovery() when 't' then null else pg_current_wal_lsn() end) AS pg_current_wal_lsn,
				(case pg_is_in_recovery() when 't' then null else pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)::float end) AS pg_wal_lsn_diff
			FROM pg_stat_replication
			`,
		},
		{
			semver.MustParseRange(">=9.2.0 <10.0.0"),
			`
			SELECT *,
				(case pg_is_in_recovery() when 't' then null else pg_current_xlog_location() end) AS pg_current_xlog_location,
				(case pg_is_in_recovery() when 't' then null else pg_xlog_location_diff(pg_current_xlog_location(), replay_location)::float end) AS pg_xlog_location_diff
			FROM pg_stat_replication
			`,
		},
		{
			semver.MustParseRange("<9.2.0"),
			`
			SELECT *,
				(case pg_is_in_recovery() when 't' then null else pg_current_xlog_location() end) AS pg_current_xlog_location
			FROM pg_stat_replication
			`,
		},
	},

	"pg_stat_activity": {
		// This query only works
		{
			semver.MustParseRange(">=9.2.0"),
			`
			SELECT
				pg_database.datname,
				tmp.state,
				COALESCE(count,0) as count,
				COALESCE(max_tx_duration,0) as max_tx_duration
			FROM
				(
				  VALUES ('active'),
				  		 ('idle'),
				  		 ('idle in transaction'),
				  		 ('idle in transaction (aborted)'),
				  		 ('fastpath function call'),
				  		 ('disabled')
				) AS tmp(state) CROSS JOIN pg_database
			LEFT JOIN
			(
				SELECT
					datname,
					state,
					count(*) AS count,
					MAX(EXTRACT(EPOCH FROM now() - xact_start))::float AS max_tx_duration
				FROM pg_stat_activity GROUP BY datname,state) AS tmp2
				ON tmp.state = tmp2.state AND pg_database.datname = tmp2.datname
			`,
		},
		{
			semver.MustParseRange("<9.2.0"),
			`
			SELECT
				datname,
				'unknown' AS state,
				COALESCE(count(*),0) AS count,
				COALESCE(MAX(EXTRACT(EPOCH FROM now() - xact_start))::float,0) AS max_tx_duration
			FROM pg_stat_activity GROUP BY datname
			`,
		},
	},
}

// Convert the query override file to the version-specific query override file
// for the exporter.
func makeQueryOverrideMap(pgVersion semver.Version, queryOverrides map[string][]OverrideQuery) map[string]string {
	resultMap := make(map[string]string)
	for name, overrideDef := range queryOverrides {
		// Find a matching semver. We make it an error to have overlapping
		// ranges at test-time, so only 1 should ever match.
		matched := false
		for _, queryDef := range overrideDef {
			if queryDef.versionRange(pgVersion) {
				resultMap[name] = queryDef.query
				matched = true
				break
			}
		}
		if !matched {
			log.Warnln("No query matched override for", name, "- disabling metric space.")
			resultMap[name] = ""
		}
	}

	return resultMap
}

// Add queries to the builtinMetricMaps and queryOverrides maps. Added queries do not
// respect version requirements, because it is assumed that the user knows
// what they are doing with their version of postgres.
//
// This function modifies metricMap and queryOverrideMap to contain the new
// queries.
// TODO: test code for all cu.
// TODO: use proper struct type system
// TODO: the YAML this supports is "non-standard" - we should move away from it.
func addQueries(content []byte, pgVersion semver.Version, server *Server) error {
	var extra map[string]interface{}

	err := yaml.Unmarshal(content, &extra)
	if err != nil {
		return err
	}

	// Stores the loaded map representation
	metricMaps := make(map[string]map[string]ColumnMapping)
	newQueryOverrides := make(map[string]string)

	for metric, specs := range extra {
		log.Debugln("New user metric namespace from YAML:", metric)
		for key, value := range specs.(map[interface{}]interface{}) {
			switch key.(string) {
			case "query":
				query := value.(string)
				newQueryOverrides[metric] = query

			case "metrics":
				for _, c := range value.([]interface{}) {
					column := c.(map[interface{}]interface{})

					for n, a := range column {
						var columnMapping ColumnMapping

						// Fetch the metric map we want to work on.
						metricMap, ok := metricMaps[metric]
						if !ok {
							// Namespace for metric not found - add it.
							metricMap = make(map[string]ColumnMapping)
							metricMaps[metric] = metricMap
						}

						// Get name.
						name := n.(string)

						for attrKey, attrVal := range a.(map[interface{}]interface{}) {
							switch attrKey.(string) {
							case "usage":
								usage, err := stringToColumnUsage(attrVal.(string))
								if err != nil {
									return err
								}
								columnMapping.usage = usage
							case "description":
								columnMapping.description = attrVal.(string)
							}
						}

						// TODO: we should support cu
						columnMapping.mapping = nil
						// Should we support this for users?
						columnMapping.supportedVersions = nil

						metricMap[name] = columnMapping
					}
				}
			}
		}
	}

	// Convert the loaded metric map into exporter representation
	partialExporterMap := makeDescMap(pgVersion, server.labels, metricMaps)

	// Merge the two maps (which are now quite flatteend)
	for k, v := range partialExporterMap {
		_, found := server.metricMap[k]
		if found {
			log.Debugln("Overriding metric", k, "from user YAML file.")
		} else {
			log.Debugln("Adding new metric", k, "from user YAML file.")
		}
		server.metricMap[k] = v
	}

	// Merge the query override map
	for k, v := range newQueryOverrides {
		_, found := server.queryOverrides[k]
		if found {
			log.Debugln("Overriding query override", k, "from user YAML file.")
		} else {
			log.Debugln("Adding new query override", k, "from user YAML file.")
		}
		server.queryOverrides[k] = v
	}

	return nil
}

func queryDatabases(server *Server) ([]string, error) {
	ctx := context.Background()
	conn, err := server.db.Acquire(context.Background())
	if err != nil {
		log.Errorf("unable to acquire db connect: %v", err)
		return nil, err
	}
	defer conn.Release()

	rows, err := conn.Conn().Query(ctx, "SELECT datname FROM pg_database WHERE datallowconn = true AND datistemplate = false") // nolint: safesql
	if err != nil {
		return nil, fmt.Errorf("Error retrieving databases: %v", err)
	}
	defer rows.Close() // nolint: errcheck

	var databaseName string
	result := make([]string, 0)
	for rows.Next() {
		err = rows.Scan(&databaseName)
		if err != nil {
			return nil, errors.New(fmt.Sprintln("Error retrieving rows:", err))
		}
		result = append(result, databaseName)
	}

	return result, nil
}

// Query within a namespace mapping and emit metrics. Returns fatal errors if
// the scrape fails, and a slice of errors if they were non-fatal.
func queryNamespaceMapping(ch chan<- prometheus.Metric, server *Server, namespace string, mapping MetricMapNamespace) ([]error, error) {
	ctx := context.Background()
	conn, err := server.db.Acquire(context.Background())
	if err != nil {
		log.Errorf("unable to acquire db connect: %v", err)
		return nil, err
	}
	defer conn.Release()

	// Check for a query override for this namespace
	query, found := server.queryOverrides[namespace]

	// Was this query disabled (i.e. nothing sensible can be queried on cu
	// version of PostgreSQL?
	if query == "" && found {
		// Return success (no pertinent data)
		return []error{}, nil
	}

	// Don't fail on a bad scrape of one metric
	var rows pgx.Rows

	var fields []pgproto3.FieldDescription

	if !found {
		query = fmt.Sprintf("SELECT * FROM %s;", namespace)
	}
	var rowCount = 0
	rows, err = conn.Conn().Query(ctx, query) // nolint: safesql
	if err != nil {
		return []error{}, fmt.Errorf("Error running query on database %q: %s %v", server, namespace, err)
	}
	defer rows.Close() // nolint: errcheck
	for rows.Next() {
		if rowCount == 0 {
			fields = rows.FieldDescriptions()
			break
		}
		rowCount++
	}

	//defer rows.Close() // nolint: errcheck

	var columnNames []string

	for _, v := range fields {
		columnNames = append(columnNames, string(v.Name))

	}

	// Make a lookup map for the column indices
	var columnIdx = make(map[string]int, len(columnNames))
	for i, n := range columnNames {
		columnIdx[n] = i
	}

	var columnData = make([]interface{}, len(columnNames))
	var scanArgs = make([]interface{}, len(columnNames))
	for i := range columnData {
		scanArgs[i] = &columnData[i]
	}

	nonfatalErrors := []error{}
	for rows.Next() {
		columnData, err = rows.Values()
		//		err = rows.Scan(scanArgs...)
		if err != nil {
			return []error{}, errors.New(fmt.Sprintln("Error retrieving rows:", namespace, err))
		}

		// Get the label values for this row.
		labels := make([]string, len(mapping.labels))
		for idx, label := range mapping.labels {
			labels[idx], _ = DBToString(columnData[columnIdx[label]])
		}

		// Loop over column names, and match to scan data. Unknown columns
		// will be filled with an untyped metric number *if* they can be
		// converted to float64s. NULLs are allowed and treated as NaN.
		for idx, columnName := range columnNames {
			if metricMapping, ok := mapping.columnMappings[columnName]; ok {
				// Is this a metricy metric?
				if metricMapping.discard {
					continue
				}

				value, ok := DBToFloat64(columnData[idx])
				if !ok {
					nonfatalErrors = append(nonfatalErrors, errors.New(fmt.Sprintln("Unexpected error parsing column: ", namespace, columnName, columnData[idx])))
					continue
				}

				// Generate the metric
				ch <- prometheus.MustNewConstMetric(metricMapping.desc, metricMapping.vtype, value, labels...)
			} else {
				// Unknown metric. Report as untyped if scan to float64 works, else note an error too.
				metricLabel := fmt.Sprintf("%s_%s", namespace, columnName)
				desc := prometheus.NewDesc(metricLabel, fmt.Sprintf("Unknown metric from %s", namespace), mapping.labels, server.labels)

				// Its not an error to fail here, since the values are
				// unexpected anyway.
				value, ok := DBToFloat64(columnData[idx])
				if !ok {
					nonfatalErrors = append(nonfatalErrors, errors.New(fmt.Sprintln("Unparseable column type - discarding: ", namespace, columnName, err)))
					continue
				}
				ch <- prometheus.MustNewConstMetric(desc, prometheus.UntypedValue, value, labels...)
			}
		}
	}
	return nonfatalErrors, nil
}

// Iterate through all the namespace mappings in the exporter and run their
// queries.
func queryNamespaceMappings(ch chan<- prometheus.Metric, server *Server) map[string]error {
	// Return a map of namespace -> errors
	namespaceErrors := make(map[string]error)

	for namespace, mapping := range server.metricMap {
		log.Debugln("Querying namespace: ", namespace)
		nonFatalErrors, err := queryNamespaceMapping(ch, server, namespace, mapping)
		// Serious error - a namespace disappeared
		if err != nil {
			namespaceErrors[namespace] = err
			log.Infoln(err)
		}
		// Non-serious errors - likely version or parsing problems.
		if len(nonFatalErrors) > 0 {
			for _, err := range nonFatalErrors {
				log.Infoln(err.Error())
			}
		}
	}

	return namespaceErrors
}
