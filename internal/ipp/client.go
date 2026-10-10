package ipp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Timeout is the total budget of one GetPrinterAttributes call.
const Timeout = 2 * time.Second

// MaxBody caps the response body that is read and decoded.
const MaxBody = 64 << 10

// StatusNotFound is client-error-not-found (wrong printer URI path).
const StatusNotFound uint16 = 0x0406

// Paths are tried in order; the next one is used only when the previous
// answers HTTP 404 or IPP client-error-not-found.
var Paths = []string{"/ipp/print", "/"}

// RequestedAttributes is the requested-attributes list sent to printers.
var RequestedAttributes = []string{
	"printer-name", "printer-info", "printer-location", "printer-make-and-model",
	"printer-device-id", "printer-state", "printer-state-reasons", "printer-state-message",
	"marker-names", "marker-levels", "marker-low-levels", "marker-high-levels",
	"marker-colors", "marker-types", "printer-input-tray", "printer-impressions-completed",
}

// ErrNotPrinter is returned when the reply has no printer attributes.
var ErrNotPrinter = errors.New("ipp: no printer attributes in response")

// Client performs IPP requests. The zero value uses a private HTTP client
// without proxy, keep-alives or redirects.
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration // <=0 means Timeout
}

var defaultHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: Timeout}).DialContext,
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  Timeout,
		MaxResponseHeaderBytes: 16 << 10,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// GetPrinterAttributes sends one Get-Printer-Attributes request to
// ip:port (normally 631) with a 2s budget and maps the answer.
func GetPrinterAttributes(ctx context.Context, ip string, port int) (*Result, error) {
	var c Client
	return c.GetPrinterAttributes(ctx, ip, port)
}

// GetPrinterAttributes: see the package-level function. A response that is
// cut off at MaxBody or malformed part-way still yields a Result (with a
// note in Errors) as long as some printer attributes were decoded.
func (c *Client) GetPrinterAttributes(ctx context.Context, ip string, port int) (*Result, error) {
	to := c.Timeout
	if to <= 0 {
		to = Timeout
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	hostport := net.JoinHostPort(ip, strconv.Itoa(port))
	var lastErr error
	for i, path := range Paths {
		res, retry, err := c.try(ctx, hc, hostport, path, uint32(i+1))
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// NewRequest builds the Get-Printer-Attributes request for printerURI.
func NewRequest(printerURI string, requestID uint32) *Message {
	req := make([]Value, len(RequestedAttributes))
	for i, a := range RequestedAttributes {
		req[i] = Value{Tag: TagKeyword, Str: a}
	}
	return &Message{Major: 1, Minor: 1, Code: OpGetPrinterAttributes, RequestID: requestID,
		Groups: []Group{{Tag: TagOperation, Attrs: []Attribute{
			{Name: "attributes-charset", Values: []Value{{Tag: TagCharset, Str: "utf-8"}}},
			{Name: "attributes-natural-language", Values: []Value{{Tag: TagLanguage, Str: "en"}}},
			{Name: "printer-uri", Values: []Value{{Tag: TagURI, Str: printerURI}}},
			{Name: "requested-attributes", Values: req},
		}}}}
}

func (c *Client) try(ctx context.Context, hc *http.Client, hostport, path string, id uint32) (res *Result, retry bool, err error) {
	body := Encode(NewRequest("ipp://"+hostport+path, id))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+hostport+path, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	hreq.Header.Set("Content-Type", "application/ipp")
	hreq.Header.Set("User-Agent", "netdiag/1.0")
	resp, err := hc.Do(hreq)
	if err != nil {
		return nil, false, fmt.Errorf("ipp: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == http.StatusNotFound, fmt.Errorf("ipp: HTTP %d", resp.StatusCode)
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	var notes []string
	if len(data) > MaxBody {
		data = data[:MaxBody]
		notes = append(notes, "ipp: response truncated at 64 KB")
	} else if rerr != nil {
		notes = append(notes, "ipp: read: "+rerr.Error())
	}
	msg, derr := Decode(data)
	if len(data) >= 8 && msg.Code > 0x00FF { // not successful-ok*

		return nil, msg.Code == StatusNotFound, fmt.Errorf("ipp: status 0x%04x", msg.Code)
	}
	r := Map(msg)
	if len(attrs(msg)) == 0 {
		if derr != nil {
			return nil, false, derr
		}
		return nil, false, ErrNotPrinter
	}
	if derr != nil {
		notes = append(notes, derr.Error())
	}
	r.Errors = notes
	return r, false, nil
}
