package pgxexporter

import (
	"sync"
)

// Servers contains a collection of servers to Postgres.
type Servers struct {
	m       sync.Mutex
	servers map[string]*Server
	opts    []ServerOpt
}

// NewServers creates a collection of servers to Postgres.
func NewServers(opts ...ServerOpt) *Servers {
	return &Servers{
		servers: make(map[string]*Server),
		opts:    opts,
	}
}

// GetServer returns established connection from a collection.
func (s *Servers) GetServer(dsn string) (*Server, error) {
	s.m.Lock()
	defer s.m.Unlock()
	var err error
	server, ok := s.servers[dsn]
	if !ok {
		server, err = NewServer(dsn, s.opts...)
		if err != nil {
			return nil, err
		}
		s.servers[dsn] = server
	}
	if err = server.Ping(); err != nil {
		delete(s.servers, dsn)
		return nil, err
	}
	return server, nil
}

// Close disconnects from all known servers.
func (s *Servers) Close() {
	s.m.Lock()
	defer s.m.Unlock()
	for _, server := range s.servers {
		server.Close()
		//if err := server.Close(); err != nil {
		//	log.Errorf("failed to close connection to %q: %v", server, err)
		//}
	}
}
