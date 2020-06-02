package pgxexporter

import (
	"context"
	"fmt"
	"github.com/blang/semver"
	"github.com/jackc/pgx/v4/pgxpool"
	//	pgx "github.com/jackc/pgx/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"sync"
)

// ServerOpt configures a server.
type ServerOpt func(*Server)

// Server describes a connection to Postgres.
// Also it contains metrics map and query overrides.
type Server struct {
	//	db *pgx.Conn
	db *pgxpool.Pool

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
	log.Debug("Configuring database with DSN:", dsn)
	config, err := pgxpool.ParseConfig(dsn)
	config.MinConns = 1
	config.MaxConns = 3

	log.Debugf("Database configuration to be used: %v", config)

	conn, err := pgxpool.ConnectConfig(context.TODO(), config)
	if err != nil {
		return nil, err
	}

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
	s.db.Close()
	log.Debug("database connection pool for this server:", s)
}

// Ping checks connection availability and possibly invalidates the connection if it fails.
func (s *Server) Ping() error {
	log.Debug("Pinging database server")
	conn, err := s.db.Acquire(context.Background())
	if err != nil {
		log.Errorf("unable to acquire db connect: %v", err)
		return err
	}
	defer conn.Release()

	conn.Conn().Ping(context.Background())
	if err := conn.Conn().Ping(context.Background()); err != nil {
		log.Errorf("Error while ping database to %q: %v", s, err)
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
