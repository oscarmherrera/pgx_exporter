module github.com/oscarmherrera/pgx_exporter

go 1.14

require (
	github.com/blang/semver v3.5.1+incompatible
	github.com/jackc/pgproto3/v2 v2.0.1
	github.com/jackc/pgx/v4 v4.6.0
	github.com/lib/pq v1.2.0
	github.com/prometheus/client_golang v1.11.1
	github.com/prometheus/client_model v0.2.0
	github.com/prometheus/common v0.26.0
	github.com/shopspring/decimal v0.0.0-20190905144223-a36b5d85f337 // indirect
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15
	gopkg.in/yaml.v2 v2.3.0
	k8s.io/api v0.18.3
	k8s.io/apimachinery v0.18.3
	sigs.k8s.io/controller-runtime v0.6.0
)
