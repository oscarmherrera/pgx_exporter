package pgxexporter

import (
	"context"
	//	"database/sql"
	"fmt"
	"github.com/blang/semver"
	pgx "github.com/jackc/pgx/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"sync"
)

// ServerOpt configures a server.
type ServerOpt func(*Server)

// Server describes a connection to Postgres.
// Also it contains metrics map and query overrides.
type Server struct {
	db *pgx.Conn
	//	db     *sql.DB
	labels prometheus.Labels

	// Last version used to calculate metric map. If mismatch on scrape,
	// then maps are recalculated.
	lastMapVersion semver.Version
	// Currently active metric map
	metricMap map[string]MetricMapNamespace
	// Currently active query overrides
	queryOverrides map[string]string
	mappingMtx     sync.RWMutex
}

// ServerWithLabels configures a set of labels.
func ServerWithLabels(labels prometheus.Labels) ServerOpt {
	return func(s *Server) {
		for k, v := range labels {
			s.labels[k] = v
		}
	}
}

// NewServer establishes a new connection using DSN.
func NewServer(dsn string, opts ...ServerOpt) (*Server, error) {
	fingerprint, err := parseFingerprint(dsn)
	if err != nil {
		return nil, err
	}

	config, err := pgx.ParseConfig(dsn)

	conn, err := pgx.ConnectConfig(context.TODO(), config)
	if err != nil {
		return nil, err
	}

	//db, err := sql.Open("postgres", dsn)
	//if err != nil {
	//	return nil, err
	//}
	//db.SetMaxOpenConns(1)
	//db.SetMaxIdleConns(1)

	log.Infof("Established new database connection to %q.", fingerprint)

	s := &Server{
		db: conn,
		labels: prometheus.Labels{
			serverLabelName: fingerprint,
		},
	}

	for _, opt := range opts {
		opt(s)
	}

	return s, nil
}

// Close disconnects from Postgres.
func (s *Server) Close() {
	err := s.db.Close(context.Background())
	if err != nil {
		log.Error("error closing db connection", err)
	}
}

// Ping checks connection availability and possibly invalidates the connection if it fails.
func (s *Server) Ping() error {

	//	defer conn.Close()

	if _, err := s.db.Exec(context.Background(), "select 1"); err != nil {
		log.Errorf("Error while closing non-pinging DB connection to %q: %v", s, err)
		return err
	}
	return nil
}

// String returns server's fingerprint.
func (s *Server) String() string {
	return s.labels[serverLabelName]
}

// Scrape loads metrics.
func (s *Server) Scrape(ch chan<- prometheus.Metric, disableSettingsMetrics bool) error {
	s.mappingMtx.RLock()
	defer s.mappingMtx.RUnlock()

	var err error

	if !disableSettingsMetrics {
		if err = querySettings(ch, s); err != nil {
			err = fmt.Errorf("error retrieving settings: %s", err)
		}
	}

	errMap := queryNamespaceMappings(ch, s)
	if len(errMap) > 0 {
		err = fmt.Errorf("queryNamespaceMappings returned %d errors", len(errMap))
	}

	return err
}
