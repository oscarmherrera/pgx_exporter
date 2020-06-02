package pgxexporter

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/blang/semver"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"io/ioutil"
	"net/url"
	"time"
)

// ColumnMapping is the user-friendly representation of a prometheus descriptor map
type ColumnMapping struct {
	usage             ColumnUsage        `yaml:"usage"`
	description       string             `yaml:"description"`
	mapping           map[string]float64 `yaml:"metric_mapping"` // Optional column mapping for MAPPEDMETRIC
	supportedVersions semver.Range       `yaml:"pg_version"`     // Semantic version ranges which are supported. Unsupported columns are not queried (internally converted to DISCARD).
}

func NewColumnMapping(u ColumnUsage, desc string, mapping *map[string]float64, ver semver.Range) *ColumnMapping {

	cm := &ColumnMapping{
		usage:             u,
		description:       desc,
		supportedVersions: ver,
	}
	if mapping != nil {
		cm.mapping = *mapping
	}
	return cm
}

func (cm *ColumnMapping) GetSupportedVersions() semver.Range {
	return cm.supportedVersions
}

func (cm *ColumnMapping) SetSupportedVersions(ver semver.Range) {
	cm.supportedVersions = ver
}

// Exporter collects Postgres metrics. It implements prometheus.Collector.
type Exporter struct {
	// Holds a reference to the build in column mappings. Currently this is for testing purposes
	// only, since it just points to the global.
	builtinMetricMaps map[string]map[string]ColumnMapping

	disableDefaultMetrics, disableSettingsMetrics, autoDiscoverDatabases, buildURI bool

	excludeDatabases []string
	dsn              []string
	userQueriesPath  string
	constantLabels   prometheus.Labels
	duration         prometheus.Gauge
	error            prometheus.Gauge
	psqlUp           prometheus.Gauge
	userQueriesError *prometheus.GaugeVec
	totalScrapes     prometheus.Counter

	// servers are used to allow re-using the DB connection between scrapes.
	// servers contains metrics map and query overrides.
	servers *Servers
}

// NewExporter returns a new PostgreSQL exporter for the provided DSN.
func NewExporter(dsn []string, opts ...ExporterOpt) *Exporter {
	e := &Exporter{
		dsn:               dsn,
		builtinMetricMaps: builtinMetricMaps,
	}

	for _, opt := range opts {
		opt(e)
	}

	e.setupInternalMetrics()
	e.setupServers()

	return e
}

func (e *Exporter) CloseAllServers() {
	e.servers.Close()
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)

	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	ch <- e.psqlUp
	e.userQueriesError.Collect(ch)
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

// Check and update the exporters query maps if the version has changed.
func (e *Exporter) checkMapVersions(ch chan<- prometheus.Metric, server *Server) error {
	ctx := context.Background()
	conn, err := server.db.Acquire(context.Background())
	if err != nil {
		log.Errorf("unable to acquire db connect: %v", err)
		return err
	}
	defer conn.Release()

	log.Debugf("Querying Postgres Version on %q", server)
	versionRow := conn.Conn().QueryRow(ctx, "SELECT version();")
	var versionString string
	err = versionRow.Scan(&versionString)
	if err != nil {
		log.Debugf("unable to select version: %v", err)
		return fmt.Errorf("Error scanning version string on %q: %v", server, err)
	}
	semanticVersion, err := parseVersion(versionString)
	if err != nil {
		log.Debugf("unable to parse semantic version: %v", err)
		return fmt.Errorf("Error parsing version string on %q: %v", server, err)
	}
	if !e.disableDefaultMetrics && semanticVersion.LT(lowestSupportedVersion) {
		log.Warnf("PostgreSQL version is lower on %q then our lowest supported version! Got %s minimum supported is %s.", server, semanticVersion, lowestSupportedVersion)
	}

	// Check if semantic version changed and recalculate maps if needed.
	if semanticVersion.NE(server.lastMapVersion) || server.metricMap == nil {
		log.Infof("Semantic Version Changed on %q: %s -> %s", server, server.lastMapVersion, semanticVersion)
		server.mappingMtx.Lock()

		if e.disableDefaultMetrics {
			server.metricMap = make(map[string]MetricMapNamespace)
			server.queryOverrides = make(map[string]string)
		} else {
			server.metricMap = makeDescMap(semanticVersion, server.labels, e.builtinMetricMaps)
			server.queryOverrides = makeQueryOverrideMap(semanticVersion, queryOverrides)
		}

		server.lastMapVersion = semanticVersion

		if e.userQueriesPath != "" {
			// Clear the metric while a reload is happening
			e.userQueriesError.Reset()

			// Calculate the hashsum of the useQueries
			userQueriesData, err := ioutil.ReadFile(e.userQueriesPath)
			if err != nil {
				log.Errorln("Failed to reload user queries:", e.userQueriesPath, err)
				e.userQueriesError.WithLabelValues(e.userQueriesPath, "").Set(1)
			} else {
				hashsumStr := fmt.Sprintf("%x", sha256.Sum256(userQueriesData))

				if err := addQueries(userQueriesData, semanticVersion, server); err != nil {
					log.Errorln("Failed to reload user queries:", e.userQueriesPath, err)
					e.userQueriesError.WithLabelValues(e.userQueriesPath, hashsumStr).Set(1)
				} else {
					// Mark user queries as successfully loaded
					e.userQueriesError.WithLabelValues(e.userQueriesPath, hashsumStr).Set(0)
				}
			}
		}

		server.mappingMtx.Unlock()
	}

	// Output the version as a special metric
	versionDesc := prometheus.NewDesc(fmt.Sprintf("%s_%s", namespace, staticLabelName),
		"Version string as reported by postgres", []string{"version", "short_version"}, server.labels)

	if !e.disableDefaultMetrics {
		ch <- prometheus.MustNewConstMetric(versionDesc,
			prometheus.UntypedValue, 1, versionString, semanticVersion.String())
	}
	return nil
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
	}(time.Now())

	e.totalScrapes.Inc()

	dsns := e.dsn
	if e.autoDiscoverDatabases {
		dsns = e.discoverDatabaseDSNs()
	}

	var errorsCount int
	var connectionErrorsCount int

	for _, dsn := range dsns {
		if err := e.scrapeDSN(ch, dsn); err != nil {
			errorsCount++

			log.Errorf(err.Error())

			if _, ok := err.(*ErrorConnectToServer); ok {
				connectionErrorsCount++
			}
		}
	}

	switch {
	case connectionErrorsCount >= len(dsns):
		e.psqlUp.Set(0)
	default:
		e.psqlUp.Set(1) // Didn't fail, can mark connection as up for this scrape.
	}

	switch errorsCount {
	case 0:
		e.error.Set(0)
	default:
		e.error.Set(1)
	}
}

func (e *Exporter) discoverDatabaseDSNs() []string {
	dsns := make(map[string]struct{})
	for _, dsn := range e.dsn {
		parsedDSN, err := url.Parse(dsn)
		if err != nil {
			log.Errorf("Unable to parse DSN (%s): %v", loggableDSN(dsn), err)
			continue
		}

		dsns[dsn] = struct{}{}
		server, err := e.servers.GetServer(dsn)
		if err != nil {
			log.Errorf("Error opening connection to database (%s): %v", loggableDSN(dsn), err)
			continue
		}

		databaseNames, err := queryDatabases(server)
		if err != nil {
			log.Errorf("Error querying databases (%s): %v", loggableDSN(dsn), err)
			continue
		}
		for _, databaseName := range databaseNames {
			if contains(e.excludeDatabases, databaseName) {
				log.Debugf("database is being excluded: %s", databaseName)
				continue
			}
			parsedDSN.Path = databaseName
			dsns[parsedDSN.String()] = struct{}{}
		}
	}

	result := make([]string, len(dsns))
	index := 0
	for dsn := range dsns {
		result[index] = dsn
		index++
	}

	return result
}

func (e *Exporter) scrapeDSN(ch chan<- prometheus.Metric, dsn string) error {
	server, err := e.servers.GetServer(dsn)
	if err != nil {
		log.Debugf("Get of the server for dsn failed: %s", dsn)
		return &ErrorConnectToServer{fmt.Sprintf("Error opening connection to database (%s): %s", loggableDSN(dsn), err)}
	}

	// Check if map versions need to be updated
	if err := e.checkMapVersions(ch, server); err != nil {
		log.Warnln("Proceeding with outdated query maps, as the Postgres version could not be determined:", err)
	}

	return server.Scrape(ch, e.disableSettingsMetrics)
}

// TODO: revisit this with the semver system
func (e *Exporter) dumpMaps() {
	for name, cmap := range builtinMetricMaps {
		query, ok := queryOverrides[name]
		if !ok {
			fmt.Println(name)
		} else {
			for _, queryOverride := range query {
				fmt.Println(name, queryOverride.versionRange, queryOverride.query)
			}
		}

		for column, details := range cmap {
			fmt.Printf("  %-40s %v\n", column, details)
		}
		fmt.Println()
	}
}

func (e *Exporter) setupServers() {
	e.servers = NewServers(ServerWithLabels(e.constantLabels))
}

func (e *Exporter) setupInternalMetrics() {
	e.duration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   exporter,
		Name:        "last_scrape_duration_seconds",
		Help:        "Duration of the last scrape of metrics from PostgresSQL.",
		ConstLabels: e.constantLabels,
	})
	e.totalScrapes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   namespace,
		Subsystem:   exporter,
		Name:        "scrapes_total",
		Help:        "Total number of times PostgresSQL was scraped for metrics.",
		ConstLabels: e.constantLabels,
	})
	e.error = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   exporter,
		Name:        "last_scrape_error",
		Help:        "Whether the last scrape of metrics from PostgreSQL resulted in an error (1 for error, 0 for success).",
		ConstLabels: e.constantLabels,
	})
	e.psqlUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Name:        "up",
		Help:        "Whether the last scrape of metrics from PostgreSQL was able to connect to the server (1 for yes, 0 for no).",
		ConstLabels: e.constantLabels,
	})
	e.userQueriesError = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   exporter,
		Name:        "user_queries_load_error",
		Help:        "Whether the user queries file was loaded and parsed successfully (1 for error, 0 for success).",
		ConstLabels: e.constantLabels,
	}, []string{"filename", "hashsum"})
}
