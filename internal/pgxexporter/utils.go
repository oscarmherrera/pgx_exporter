package pgxexporter

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"io/ioutil"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// UnmarshalYAML implements the yaml.Unmarshaller interface.
func (cu *ColumnUsage) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var value string
	if err := unmarshal(&value); err != nil {
		return err
	}

	columnUsage, err := stringToColumnUsage(value)
	if err != nil {
		return err
	}

	*cu = columnUsage
	return nil
}

// UnmarshalYAML implements yaml.Unmarshaller
func (cm *ColumnMapping) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain ColumnMapping
	return unmarshal((*plain)(cm))
}

func PrintExporterMaps(exp *Exporter) {
	exp.dumpMaps()
}

// convert a string to the corresponding ColumnUsage
func stringToColumnUsage(s string) (ColumnUsage, error) {
	var u ColumnUsage
	var err error
	switch s {
	case "DISCARD":
		u = DISCARD

	case "LABEL":
		u = LABEL

	case "COUNTER":
		u = COUNTER

	case "GAUGE":
		u = GAUGE

	case "MAPPEDMETRIC":
		u = MAPPEDMETRIC

	case "DURATION":
		u = DURATION

	default:
		err = fmt.Errorf("wrong ColumnUsage given : %s", s)
	}

	return u, err
}

// DBToFloat64 Convert database.sql types to float64s for Prometheus consumption. Null types are mapped to NaN. string and []byte
// types are mapped as NaN and !ok
func DBToFloat64(t interface{}) (float64, bool) {
	switch v := t.(type) {
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case time.Time:
		return float64(v.Unix()), true
	case []byte:
		// Try and convert to string and then parse to a float64
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			log.Infoln("Could not parse []byte:", err)
			return math.NaN(), false
		}
		return result, true
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			log.Infoln("Could not parse string:", err)
			return math.NaN(), false
		}
		return result, true
	case bool:
		if v {
			return 1.0, true
		}
		return 0.0, true
	case nil:
		return math.NaN(), true
	default:
		return math.NaN(), false
	}
}

// DBToString Convert database.sql to string for Prometheus labels. Null types are mapped to empty strings.
func DBToString(t interface{}) (string, bool) {
	switch v := t.(type) {
	case int32:
		return fmt.Sprintf("%v", v), true
	case int64:
		return fmt.Sprintf("%v", v), true
	case float64:
		return fmt.Sprintf("%v", v), true
	case time.Time:
		return fmt.Sprintf("%v", v.Unix()), true
	case nil:
		return "", true
	case []byte:
		// Try and convert to string
		return string(v), true
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

func parseFingerprint(url string) (string, error) {
	configConn, err := pgx.ParseConfig(url)

	if err != nil {
		log.Errorf("Unable to parse URI %s.", url)
		return "", err
	}

	var fingerprint string

	if configConn.Host == "" {
		fingerprint += "localhost"
	} else {
		fingerprint += configConn.Host
	}

	if configConn.Port == 0 {
		fingerprint += ":" + "5432"
	} else {
		fingerprint += ":" + strconv.Itoa(int(configConn.Port))
	}

	if configConn.Host == "" && configConn.Port == 0 {
		errorString := fmt.Sprintf("malformed dsn \"%s\"", url)
		return "", errors.New(errorString)

	}

	return fingerprint, nil
}

func loggableDSN(dsn string) string {
	pDSN, err := url.Parse(dsn)
	if err != nil {
		return "could not parse DATA_SOURCE_NAME"
	}
	// Blank user info if not nil
	if pDSN.User != nil {
		pDSN.User = url.UserPassword(pDSN.User.Username(), "PASSWORD_REMOVED")
	}

	return pDSN.String()
}

func parseConstLabels(s string) prometheus.Labels {
	labels := make(prometheus.Labels)

	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return labels
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		keyValue := strings.Split(strings.TrimSpace(p), "=")
		if len(keyValue) != 2 {
			log.Errorf(`Wrong constant labels format %q, should be "key=value"`, p)
			continue
		}
		key := strings.TrimSpace(keyValue[0])
		value := strings.TrimSpace(keyValue[1])
		if key == "" || value == "" {
			continue
		}
		labels[key] = value
	}

	return labels
}

// ErrorConnectToServer is a connection to PgSQL server error
type ErrorConnectToServer struct {
	Msg string
}

// try to get the DataSource
// DATA_SOURCE_NAME always wins so we do not break older versions
// reading secrets from files wins over secrets in environment variables
// DATA_SOURCE_NAME > DATA_SOURCE_{USER|PASS}_FILE > DATA_SOURCE_{USER|PASS}
func getDataSources() []string {
	var dsn = os.Getenv("DATA_SOURCE_NAME")
	if len(dsn) == 0 {
		var user string
		var pass string

		if len(os.Getenv("DATA_SOURCE_USER_FILE")) != 0 {
			fileContents, err := ioutil.ReadFile(os.Getenv("DATA_SOURCE_USER_FILE"))
			if err != nil {
				panic(err)
			}
			user = strings.TrimSpace(string(fileContents))
		} else {
			user = os.Getenv("DATA_SOURCE_USER")
		}

		if len(os.Getenv("DATA_SOURCE_PASS_FILE")) != 0 {
			fileContents, err := ioutil.ReadFile(os.Getenv("DATA_SOURCE_PASS_FILE"))
			if err != nil {
				panic(err)
			}
			pass = strings.TrimSpace(string(fileContents))
		} else {
			pass = os.Getenv("DATA_SOURCE_PASS")
		}

		ui := url.UserPassword(user, pass).String()
		uri := os.Getenv("DATA_SOURCE_URI")
		dsn = "postgresql://" + ui + "@" + uri

		return []string{dsn}
	}
	return strings.Split(dsn, ",")
}

func contains(a []string, x string) bool {
	for _, n := range a {
		if x == n {
			return true
		}
	}
	return false
}

/*
*
*  Putting all public functions below this line just cleaning up early dumb oscar code
*  when everything was kinda of scattered
*
 */

func GetDataSources() []string {
	return getDataSources()
}

// Error returns error
func (e *ErrorConnectToServer) Error() string {
	return e.Msg
}

func ParseFingerprint(url string) (string, error) {
	return parseFingerprint(url)
}

func ParseConstLabels(s string) prometheus.Labels {
	return parseConstLabels(s)
}
