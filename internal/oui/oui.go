// Package oui maps MAC address prefixes to vendor names using an embedded
// gzipped IEEE MA-L registry (see scripts/update-oui.sh).
package oui

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/csv"
	"io"
	"strings"
	"sync"
)

//go:embed oui.csv.gz
var data []byte

var (
	once sync.Once
	db   map[string]string
)

func load() {
	db = make(map[string]string, 40000)
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return
	}
	defer zr.Close()
	r := csv.NewReader(zr)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return
		}
		if err != nil || len(rec) < 2 {
			continue
		}
		db[strings.ToUpper(strings.TrimSpace(rec[0]))] = strings.TrimSpace(rec[1])
	}
}

// Lookup returns the vendor for a MAC in any common format (":", "-", ".",
// or bare hex; unpadded octets accepted with separators). Returns "" if unknown.
func Lookup(mac string) string {
	once.Do(load)
	p, ok := prefix(mac)
	if !ok {
		return ""
	}
	return db[p]
}

func prefix(mac string) (string, bool) {
	mac = strings.TrimSpace(mac)
	var hex string
	if strings.ContainsAny(mac, ":-") {
		parts := strings.FieldsFunc(mac, func(r rune) bool { return r == ':' || r == '-' })
		if len(parts) < 3 {
			return "", false
		}
		for _, p := range parts[:3] {
			if len(p) == 1 {
				p = "0" + p
			}
			hex += p
		}
	} else {
		hex = strings.ReplaceAll(mac, ".", "")
		if len(hex) < 6 {
			return "", false
		}
		hex = hex[:6]
	}
	if len(hex) != 6 {
		return "", false
	}
	hex = strings.ToUpper(hex)
	for _, c := range hex {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return hex, true
}
