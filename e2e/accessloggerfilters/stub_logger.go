// Package accessloggerfilters provides the e2e access logger stub.
// It registers an access logger named "e2e-logger" that POSTs each log
// event as JSON to a sink URL supplied in the logger config.
// This avoids the shared-channel problem: the .so and the test binary
// are separate Go runtimes with separate package state.
//
// Config JSON: {"sink_url": "http://127.0.0.1:PORT"}
package accessloggerfilters

import (
	"bytes"
	"encoding/json"
	"net/http"

	luwes "github.com/dio/luwes"
	"github.com/dio/luwes/shared"
)

func init() {
	luwes.RegisterAccessLogger("e2e-logger", newFactory)
}

type config struct {
	SinkURL string `json:"sink_url"`
}

func newFactory(_ shared.AccessLoggerConfigHandle, raw []byte) (shared.AccessLoggerFactory, error) {
	var cfg config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &factory{sinkURL: cfg.SinkURL + "/log"}, nil
}

type factory struct{ sinkURL string }

func (f *factory) NewLogger() shared.AccessLogger { return &logger{sinkURL: f.sinkURL} }
func (f *factory) OnDestroy()                     {}

type logger struct {
	shared.EmptyAccessLogger
	sinkURL string
}

type entry struct {
	LogType       int32   `json:"log_type"`
	DurationMs    float64 `json:"duration_ms"`
	BytesSent     uint64  `json:"bytes_sent"`
	BytesReceived uint64  `json:"bytes_received"`
	ResponseCode  uint32  `json:"response_code"`
	ResponseFlags uint64  `json:"response_flags"`
	CodeDetails   string  `json:"code_details"`
	RequestID     string  `json:"request_id"`
}

func (l *logger) OnLog(h shared.AccessLoggerHandle, logType shared.AccessLogType) {
	if l.sinkURL == "" {
		return
	}
	timing := h.GetTimingInfo()
	b := h.GetBytesInfo()
	code := h.GetResponseCode()
	flags := h.GetResponseFlags()

	codeDetails := ""
	if v, ok := h.GetAttributeString(shared.AttributeIDResponseCodeDetails); ok {
		codeDetails = v.ToString()
	}
	reqID := ""
	if v, ok := h.GetHeader(shared.HttpHeaderTypeRequest, "x-request-id"); ok {
		reqID = v.ToString()
	}

	e := entry{
		LogType:       int32(logType),
		DurationMs:    float64(timing.RequestCompleteDurationNs) / 1e6,
		BytesSent:     b.BytesSent,
		BytesReceived: b.BytesReceived,
		ResponseCode:  code,
		ResponseFlags: flags,
		CodeDetails:   codeDetails,
		RequestID:     reqID,
	}

	body, err := json.Marshal(e)
	if err != nil {
		return
	}
	// Best-effort POST; ignore errors (test will time out on failure).
	resp, err := http.Post(l.sinkURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err == nil {
		resp.Body.Close()
	}
}
