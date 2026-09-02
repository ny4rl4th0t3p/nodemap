// Package geo maps an IP to coarse, non-identifying metadata: a country
// code and an autonomous system. That is the whole enrichment surface; city
// and finer location are deliberately not decoded.
//
// The MMDB backend reads DB-IP Lite or GeoLite2 files, which share the
// MaxMind-compatible schema (country.iso_code, autonomous_system_number,
// autonomous_system_organization). DB-IP Lite is CC BY 4.0: any page that
// shows results derived from it must carry the DB-IP attribution link.
package geo

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/ny4rl4th0t3p/nodemap/internal/crawl"
)

// ErrNoDatabase is returned by Open when no path is given.
var ErrNoDatabase = errors.New("geo: no database path given")

// None is the backend that knows nothing: every lookup is unknown.
type None struct{}

// Lookup implements crawl.Enricher.
func (None) Lookup(string) crawl.Enrichment { return crawl.Enrichment{} }

// MMDB looks up country and ASN in two MaxMind-format databases. Either may
// be absent, in which case that dimension stays unknown. Lookups are safe
// for concurrent use; Close must not run until the crawl has finished.
type MMDB struct {
	country *maxminddb.Reader
	asn     *maxminddb.Reader
}

// Open opens whichever of the two databases has a non-empty path.
func Open(countryPath, asnPath string) (*MMDB, error) {
	if countryPath == "" && asnPath == "" {
		return nil, ErrNoDatabase
	}
	m := &MMDB{}
	var err error
	if countryPath != "" {
		if m.country, err = maxminddb.Open(countryPath); err != nil {
			return nil, fmt.Errorf("geo: country database: %w", err)
		}
	}
	if asnPath != "" {
		if m.asn, err = maxminddb.Open(asnPath); err != nil {
			_ = m.Close()
			return nil, fmt.Errorf("geo: asn database: %w", err)
		}
	}
	return m, nil
}

// Close releases both databases.
func (m *MMDB) Close() error {
	var errs []error
	if m.country != nil {
		errs = append(errs, m.country.Close())
		m.country = nil
	}
	if m.asn != nil {
		errs = append(errs, m.asn.Close())
		m.asn = nil
	}
	return errors.Join(errs...)
}

type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup implements crawl.Enricher. An unparsable IP, an IP absent from a
// database, or a record that does not decode all yield unknown for that
// dimension; enrichment never fails a crawl.
func (m *MMDB) Lookup(ip string) crawl.Enrichment {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return crawl.Enrichment{}
	}
	addr = addr.Unmap()
	var e crawl.Enrichment
	if m.country != nil {
		var rec countryRecord
		if err := m.country.Lookup(addr).Decode(&rec); err == nil {
			e.Country = rec.Country.ISOCode
		}
	}
	if m.asn != nil {
		var rec asnRecord
		if err := m.asn.Lookup(addr).Decode(&rec); err == nil {
			e.ASN = rec.Number
			e.ASOrg = rec.Org
		}
	}
	return e
}
