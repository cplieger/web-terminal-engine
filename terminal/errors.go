package terminal

import (
	"maps"
	"net/http"

	"github.com/cplieger/go/httpserver/v2"
)

// The codes the session routes refuse with besides httpserver's own
// cross_origin_refused, which a WebSocket upgrade from a disallowed Origin
// answers. Each names its route's subject, so an application catalog that also
// owns another library's codes holds no two specs under one code.
const (
	codeSessionUnknown           httpserver.ErrorCode = "session_unknown"
	codeSessionCreateFailed      httpserver.ErrorCode = "session_create_failed"
	codeSessionShuttingDown      httpserver.ErrorCode = "session_shutting_down"
	codeSessionBodyInvalid       httpserver.ErrorCode = "session_body_invalid"
	codeSessionTitleEmpty        httpserver.ErrorCode = "session_title_empty"
	codeSessionOrderStale        httpserver.ErrorCode = "session_order_stale"
	codeSessionLayoutStale       httpserver.ErrorCode = "session_layout_stale"
	codeSessionLayoutInvalid     httpserver.ErrorCode = "session_layout_invalid"
	codeSessionEventsUnsupported httpserver.ErrorCode = "session_events_unsupported"
	codeSessionEventsFull        httpserver.ErrorCode = "session_events_full"
	codeCrossOriginRefused       httpserver.ErrorCode = "cross_origin_refused"
)

// ErrorCodeRestoring is the code of the 503 the session routes answer, with
// RestoringMessage and Retry-After, while RestoreSessionSet runs. A host gate
// that holds creates before its restore starts answers the same code.
const ErrorCodeRestoring httpserver.ErrorCode = "session_restoring"

// codeSpecs holds exactly the codes this package answers that httpserver's own
// CodeSpecs does not list; httpservertest.CheckCodeSpecs holds it to the
// source. Every refusal but a failed create changes nothing, so it is safe to
// resend once fixed.
var codeSpecs = map[httpserver.ErrorCode]httpserver.CodeSpec{
	codeSessionUnknown:           {Status: http.StatusNotFound, Remediation: "List the sessions again: this one has ended or never existed.", NoEffect: true},
	codeSessionCreateFailed:      {Status: http.StatusInternalServerError, Remediation: "Retry; if it fails again, the server log line \"session: create failed\" names the cause."},
	codeSessionShuttingDown:      {Status: http.StatusServiceUnavailable, Remediation: "Retry once the server has restarted.", NoEffect: true},
	ErrorCodeRestoring:           {Status: http.StatusServiceUnavailable, Remediation: "Retry after Retry-After: the server is restoring its saved sessions.", NoEffect: true},
	codeSessionBodyInvalid:       {Status: http.StatusBadRequest, Remediation: "Send one JSON object with the fields this route documents, and nothing after it.", NoEffect: true},
	codeSessionTitleEmpty:        {Status: http.StatusBadRequest, Remediation: "Send a title with visible text, or DELETE the pinned title to clear it.", NoEffect: true},
	codeSessionOrderStale:        {Status: http.StatusConflict, Remediation: "List the sessions again, then send an order naming every live session exactly once.", NoEffect: true},
	codeSessionLayoutStale:       {Status: http.StatusConflict, Remediation: "List the sessions again, then send a layout whose sides name live sessions.", NoEffect: true},
	codeSessionLayoutInvalid:     {Status: http.StatusBadRequest, Remediation: "Fix the rule the message names, then send the layout again.", NoEffect: true},
	codeSessionEventsUnsupported: {Status: http.StatusInternalServerError, Remediation: "Report the request id to the server's operator: a middleware in front of the session events route cannot flush a stream.", NoEffect: true},
	codeSessionEventsFull:        {Status: http.StatusServiceUnavailable, Remediation: "Close another tab's session stream, or reconnect after a short wait.", NoEffect: true},
}

// CodeSpecs returns every code the session routes answer that httpserver's own
// CodeSpecs does not list, with its status and default remediation, for an
// application's error catalog. The map is the caller's own copy.
func CodeSpecs() map[httpserver.ErrorCode]httpserver.CodeSpec {
	return maps.Clone(codeSpecs)
}

// WithErrors sets the ErrorResponder that writes every refusal the manager's
// routes answer: the session REST API's, the status stream's, and the
// WebSocket route's origin refusal, for unknown sessions and for each session
// handler the manager builds. Pass an application catalog's responder to give
// each refusal the application's remediation. Nil means the default,
// httpserver.WriteError with this package's remediation for its own codes, so
// the last WithErrors wins.
func WithErrors(r httpserver.ErrorResponder) ManagerOption {
	return func(c *managerConfig) { c.errs = r }
}

// writeError is the default ErrorResponder: httpserver's envelope, which
// carries httpserver's remediation for its own codes, with this package's
// remediation for a code it owns.
func writeError(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
	body := httpserver.NewErrorResponse(r, code, msg)
	if spec, ok := codeSpecs[code]; ok {
		body.Remediation = spec.Remediation
	}
	httpserver.WriteErrorBody(w, status, body)
}
