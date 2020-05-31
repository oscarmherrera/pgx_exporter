package main

import (
	"fmt"
	pgxx "github.com/oscarmherrera/pgx_exporter/internal/pgxexporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"gopkg.in/alecthomas/kingpin.v2"
	"net/http"
	"runtime"
)

// Version is set during build to the git describe version
// (semantic version)-(commitish) form.
//var Version = "0.5.1"

var (
	listenAddress          = kingpin.Flag("web.listen-address", "Address to listen on for web interface and telemetry.").Default(":9187").Envar("PGXEXPORTER_WEB_LISTEN_ADDRESS").String()
	metricPath             = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").Envar("PGXEXPORTER_WEB_TELEMETRY_PATH").String()
	disableDefaultMetrics  = kingpin.Flag("disable-default-metrics", "Do not include default metrics.").Default("false").Envar("PGXEXPORTER_DISABLE_DEFAULT_METRICS").Bool()
	disableSettingsMetrics = kingpin.Flag("disable-settings-metrics", "Do not include pg_settings metrics.").Default("false").Envar("PGXEXPORTER_DISABLE_SETTINGS_METRICS").Bool()
	autoDiscoverDatabases  = kingpin.Flag("auto-discover-databases", "Whether to discover the databases on a server dynamically.").Default("false").Envar("PGXEXPORTER_AUTO_DISCOVER_DATABASES").Bool()
	queriesPath            = kingpin.Flag("extend.query-path", "Path to custom queries to run.").Default("").Envar("PGXEXPORTER_EXTEND_QUERY_PATH").String()
	onlyDumpMaps           = kingpin.Flag("dumpmaps", "Do not run, simply dump the maps.").Bool()
	constantLabelsList     = kingpin.Flag("constantLabels", "A list of label=value separated by comma(,).").Default("").Envar("PGXEXPORTER_CONSTANT_LABELS").String()
	excludeDatabases       = kingpin.Flag("exclude-databases", "A list of databases to remove when autoDiscoverDatabases is enabled").Default("").Envar("PGXEXPORTER_EXCLUDE_DATABASES").String()
)

func main() {
	kingpin.Version(fmt.Sprintf("pgx_exporter %s (built with %s)\n", Version, runtime.Version()))
	log.AddFlags(kingpin.CommandLine)
	kingpin.Parse()

	// landingPage contains the HTML served at '/'.
	// TODO: Make this nicer and more informative.
	var landingPage = []byte(`<html>
	<head><title>PGX exporter</title></head>
	<body>
	<h1>PGX exporter</h1>
	<p><a href='` + *metricPath + `'>Metrics</a></p>
	</body>
	</html>
	`)

	if *onlyDumpMaps {
		exp := &pgxx.Exporter{}
		pgxx.PrintExporterMaps(exp)
		return
	}

	dsn := pgxx.GetDataSources()
	if len(dsn) == 0 {
		log.Fatal("couldn't find environment variables describing the datasource to use")
	}

	exporter := pgxx.NewExporter(dsn,
		pgxx.DisableDefaultMetrics(*disableDefaultMetrics),
		pgxx.DisableSettingsMetrics(*disableSettingsMetrics),
		pgxx.AutoDiscoverDatabases(*autoDiscoverDatabases),
		pgxx.WithUserQueriesPath(*queriesPath),
		pgxx.WithConstantLabels(*constantLabelsList),
		pgxx.ExcludeDatabases(*excludeDatabases),
	)
	defer func() {
		exporter.CloseAllServers()
	}()

	prometheus.MustRegister(exporter)

	http.Handle(*metricPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "Content-Type:text/plain; charset=UTF-8") // nolint: errcheck
		_, err := w.Write(landingPage)
		if err != nil {
			log.Errorf("Unable to write landing page: %s", err)
		}

	})

	log.Infof("Starting Server: %s", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
