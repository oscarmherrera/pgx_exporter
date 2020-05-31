
# PostgreSQL Server Exporter

Prometheus exporter for PostgreSQL server metrics using jackc's pgx.

Tested on PostgreSQL distributions of EDB Advanced Server and PostgreSQL community on 
versions: `9.4`, `9.5`, `9.6`, `10`, `11`, `12`
In both VMs and Containers it supports the 

## Quick Start
This package is built for kubernetes but can run anywhere really:
```
docker example.

# Start an example database
docker run --net=host -it --rm -e POSTGRES_PASSWORD=password postgres
# Connect to it
docker run --net=host -e DATA_SOURCE_NAME="postgresql://postgres:password@localhost:5432/postgres?sslmode=disable" gcr.io/pgx_exporter
```

## Building and running

The build system is godel
```
./godelw build
export DATA_SOURCE_USER=<login>;
export DATA_SOURCE_PASS=<password>;
export DATA_SOURCE_URI=localhost:5444/edb
$ ./pgx_exporter <flags>

```

To build the dockerfile, run `go run mage.go docker`.

This will build the default container image as `gcr.io/edb-oscar/pgx_exporter.ubi:latest`. This
is a minimal docker image containing *just* pgx_exporter. By default no SSL
certificates are included, if you need to use SSL you should either bind-mount
`/etc/ssl/certs/ca-certificates.crt` or derive a new image containing them.

### Vendoring
We use go module for everythign so no need for vendorin

### Flags

* `web.listen-address`
  Address to listen on for web interface and telemetry. Default is `:9187`.

* `web.telemetry-path`
  Path under which to expose metrics. Default is `/metrics`.

* `disable-default-metrics`
  Use only metrics supplied from `queries.yaml` via `--extend.query-path`.

* `disable-settings-metrics`
  Use the flag if you don't want to scrape `pg_settings`.

* `extend.query-path`
  Path to a YAML file containing custom queries to run. Check out [`queries.yaml`](queries.yaml)
  for examples of the format.

* `dumpmaps`
  Do not run - print the internal representation of the metric maps. Useful when debugging a custom
  queries file.

* `log.level`
  Set logging level: one of `debug`, `info`, `warn`, `error`, `fatal`

* `log.format`
  Set the log output target and format. e.g. `logger:syslog?appname=bob&local=7` or `logger:stdout?json=true`
  Defaults to `logger:stderr`.

* `constantLabels`
  Labels to set in all metrics. A list of `label=value` pairs, separated by commas.

### Environment Variables

The following environment variables configure the exporter:

* `DATA_SOURCE_NAME`
  the default legacy format. Accepts URI form and key=value form arguments. The
  URI may contain the username and password to connect with.

* `DATA_SOURCE_URI`
   an alternative to `DATA_SOURCE_NAME` which exclusively accepts the raw URI
   without a username and password component.

* `DATA_SOURCE_USER`
  When using `DATA_SOURCE_URI`, this environment variable is used to specify
  the username.

* `DATA_SOURCE_USER_FILE`
  The same, but reads the username from a file.

* `DATA_SOURCE_PASS`
  When using `DATA_SOURCE_URI`, this environment variable is used to specify
  the password to connect with.

* `DATA_SOURCE_PASS_FILE`
  The same as above but reads the password from a file.

* `PGXEXPORTER_WEB_LISTEN_ADDRESS`
  Address to listen on for web interface and telemetry. Default is `:9187`.

* `PGXEXPORTER_WEB_TELEMETRY_PATH`
  Path under which to expose metrics. Default is `/metrics`.

* `PGXEXPORTER_DISABLE_DEFAULT_METRICS`
  Use only metrics supplied from `queries.yaml`. Value can be `true` or `false`. Default is `false`.

* `PGXEXPORTER_DISABLE_SETTINGS_METRICS`
  Use the flag if you don't want to scrape `pg_settings`. Value can be `true` or `false`. Defauls is `false`.

* `PGXEXPORTER_EXTEND_QUERY_PATH`
  Path to a YAML file containing custom queries to run. Check out [`queries.yaml`](queries.yaml)
  for examples of the format.

* `PGXEXPORTER_CONSTANT_LABELS`
  Labels to set in all metrics. A list of `label=value` pairs, separated by commas.

Settings set by environment variables starting with `PG_` will be overwritten by the corresponding CLI flag if given.

### Setting the Postgres server's data source name

The PostgreSQL server's [data source name](http://en.wikipedia.org/wiki/Data_source_name)
must be set via the `DATA_SOURCE_NAME` environment variable.

For running it locally on a default Debian/Ubuntu install, this will work (transpose to init script as appropriate):

    sudo -u postgres DATA_SOURCE_NAME="user=postgres host=/var/run/postgresql/ sslmode=disable" pgx_exporter

Also, you can set a list of sources to scrape different instances from the one exporter setup. Just define a comma separated string.

    sudo -u postgres DATA_SOURCE_NAME="port=5432,port=6432" pgx_exporter

See the [github.com/lib/pq](http://github.com/lib/pq) module for other ways to format the connection string.

### Adding new metrics

The exporter will attempt to dynamically export additional metrics if they are added in the
future, but they will be marked as "untyped". Additional metric maps can be easily created
from Postgres documentation by copying the tables and using the following Python snippet:

```python
x = """tab separated raw text of a documentation table"""
for l in StringIO(x):
    column, ctype, description = l.split('\t')
    print """"{0}" : {{ prometheus.CounterValue, prometheus.NewDesc("pg_stat_database_{0}", "{2}", nil, nil) }}, """.format(column.strip(), ctype, description.strip())
```
Adjust the value of the resultant prometheus value type appropriately. This helps build
rich self-documenting metrics for the exporter.

### Adding new metrics via a config file

The -extend.query-path command-line argument specifies a YAML file containing additional queries to run.
Some examples are provided in [queries.yaml](queries.yaml).

### Disabling default metrics
To work with non-officially-supported postgres versions you can try disabling (e.g. 8.2.15)
or a variant of postgres (e.g. Greenplum) you can disable the default metrics with the `--disable-default-metrics`
flag. This removes all built-in metrics, and uses only metrics defined by queries in the `queries.yaml` file you supply
(so you must supply one, otherwise the exporter will return nothing but internal statuses and not your database).

### Automatically discover databases
To scrape metrics from all databases on a database server, the database DSN's can be dynamically discovered via the 
`--auto-discover-databases` flag. When true, `SELECT datname FROM pg_database WHERE datallowconn = true AND datistemplate = false` is run for all configured DSN's. From the 
result a new set of DSN's is created for which the metrics are scraped.

In addition, the option `--exclude-databases` adds the possibily to filter the result from the auto discovery to discard databases you do not need.

### Running as non-superuser

To be able to collect metrics from `pg_stat_activity` and `pg_stat_replication`
as  non-superuser you have to create functions and views as a superuser, and
assign permissions separately to those.

In PostgreSQL, views run with the permissions of the user that created them so
they can act as security barriers. Functions need to be created to share this
data with the non-superuser. Only creating the views will leave out the most
important bits of data.

```sql
-- To use IF statements, hence to be able to check if the user exists before
-- attempting creation, we need to switch to procedural SQL (PL/pgSQL)
-- instead of standard SQL.
-- More: https://www.postgresql.org/docs/9.3/plpgsql-overview.html
-- To preserve compatibility with <9.0, DO blocks are not used; instead,
-- a function is created and dropped.
CREATE OR REPLACE FUNCTION __tmp_create_user() returns void as $$
BEGIN
  IF NOT EXISTS (
          SELECT                       -- SELECT list can stay empty for this
          FROM   pg_catalog.pg_user
          WHERE  usename = 'pgx_exporter') THEN
    CREATE USER pgx_exporter;
  END IF;
END;
$$ language plpgsql;

SELECT __tmp_create_user();
DROP FUNCTION __tmp_create_user();

ALTER USER pgx_exporter WITH PASSWORD 'password';
ALTER USER pgx_exporter SET SEARCH_PATH TO pgx_exporter,pg_catalog;

-- If deploying as non-superuser (for example in AWS RDS), uncomment the GRANT
-- line below and replace <MASTER_USER> with your root user.
-- GRANT pgx_exporter TO <MASTER_USER>;
CREATE SCHEMA IF NOT EXISTS pgx_exporter;
GRANT USAGE ON SCHEMA pgx_exporter TO pgx_exporter;

CREATE OR REPLACE FUNCTION get_pg_stat_activity() RETURNS SETOF pg_stat_activity AS
$$ SELECT * FROM pg_catalog.pg_stat_activity; $$
LANGUAGE sql
VOLATILE
SECURITY DEFINER;

CREATE OR REPLACE VIEW pgx_exporter.pg_stat_activity
AS
  SELECT * from get_pg_stat_activity();

GRANT SELECT ON pgx_exporter.pg_stat_activity TO pgx_exporter;

CREATE OR REPLACE FUNCTION get_pg_stat_replication() RETURNS SETOF pg_stat_replication AS
$$ SELECT * FROM pg_catalog.pg_stat_replication; $$
LANGUAGE sql
VOLATILE
SECURITY DEFINER;

CREATE OR REPLACE VIEW pgx_exporter.pg_stat_replication
AS
  SELECT * FROM get_pg_stat_replication();

GRANT SELECT ON pgx_exporter.pg_stat_replication TO pgx_exporter;
```

> **NOTE**
> <br />Remember to use `postgres` database name in the connection string:
> ```
> DATA_SOURCE_NAME=postgresql://pgx_exporter:password@localhost:5432/postgres?sslmode=disable
> ```
