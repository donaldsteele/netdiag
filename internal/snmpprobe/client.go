package snmpprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Kind classifies a decoded PDU value.
type Kind int

const (
	KindNull  Kind = iota // noSuchObject/noSuchInstance/endOfMibView/null
	KindInt               // integer, counter, gauge
	KindOctet             // octet string (may be binary)
	KindOID               // object identifier (in Str)
	KindTicks             // timeticks (hundredths of a second in Int)
)

// PDU is one varbind with a library-independent value.
type PDU struct {
	OID   string // no leading dot
	Kind  Kind
	Int   int64
	Bytes []byte
	Str   string // OID value for KindOID
}

// Client is the minimal read-only SNMP surface used by the probe.
type Client interface {
	Get(ctx context.Context, oids []string) ([]PDU, error)
	// Walk returns all varbinds under root.
	Walk(ctx context.Context, root string) ([]PDU, error)
	Close() error
}

// Dialer builds a Client. version is "v2c" or "v1".
type Dialer func(ctx context.Context, ip, community, version string) (Client, error)

// ErrNoResponse is returned when neither v2c nor v1 answers the sysDescr gate.
var ErrNoResponse = errors.New("snmp: no response")

type gosnmpClient struct{ g *gosnmp.GoSNMP }

// GoSNMPDialer is the production Dialer (UDP/161, 1.5s timeout, 1 retry).
func GoSNMPDialer(ctx context.Context, ip, community, version string) (Client, error) {
	g := &gosnmp.GoSNMP{
		Target:         ip,
		Port:           161,
		Community:      community,
		Version:        gosnmp.Version2c,
		Timeout:        1500 * time.Millisecond,
		Retries:        1,
		MaxRepetitions: 20,
		Context:        ctx,
	}
	if version == "v1" {
		g.Version = gosnmp.Version1
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return &gosnmpClient{g: g}, nil
}

func (c *gosnmpClient) Close() error {
	if c.g.Conn != nil {
		return c.g.Conn.Close()
	}
	return nil
}

func (c *gosnmpClient) Get(ctx context.Context, oids []string) ([]PDU, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pkt, err := c.g.Get(oids)
	if err != nil {
		return nil, err
	}
	return convert(pkt.Variables), nil
}

func (c *gosnmpClient) Walk(ctx context.Context, root string) ([]PDU, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var vars []gosnmp.SnmpPDU
	var err error
	if c.g.Version == gosnmp.Version1 {
		vars, err = c.g.WalkAll(root)
	} else {
		vars, err = c.g.BulkWalkAll(root)
	}
	if err != nil && len(vars) == 0 {
		return nil, err
	}
	return convert(vars), err
}

func convert(vars []gosnmp.SnmpPDU) []PDU {
	out := make([]PDU, 0, len(vars))
	for _, v := range vars {
		p := PDU{OID: strings.TrimPrefix(v.Name, ".")}
		switch v.Type {
		case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.Uinteger32, gosnmp.Counter64:
			p.Kind = KindInt
			p.Int = gosnmp.ToBigInt(v.Value).Int64()
		case gosnmp.TimeTicks:
			p.Kind = KindTicks
			p.Int = gosnmp.ToBigInt(v.Value).Int64()
		case gosnmp.OctetString:
			p.Kind = KindOctet
			if b, ok := v.Value.([]byte); ok {
				p.Bytes = b
			} else {
				p.Bytes = []byte(fmt.Sprint(v.Value))
			}
		case gosnmp.ObjectIdentifier:
			p.Kind = KindOID
			p.Str = strings.TrimPrefix(fmt.Sprint(v.Value), ".")
		case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
			p.Kind = KindNull
		default: // IPAddress and unknown types -> string
			p.Kind = KindOctet
			p.Bytes = []byte(fmt.Sprint(v.Value))
		}
		out = append(out, p)
	}
	return out
}
