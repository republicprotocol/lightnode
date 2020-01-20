package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/renproject/darknode/jsonrpc"
	"github.com/renproject/phi"
	"github.com/rs/cors"
	"github.com/sirupsen/logrus"
)

var (
	// ErrorCodeMaxBatchSizeExceeded is an implementation specific error code
	// that indicates that the maximum batch size has been exceeded.
	ErrorCodeMaxBatchSizeExceeded = -32001

	// ErrorCodeRateLimitExceeded is an implementation specific error code that
	// indicates that the client has been rate limited.
	ErrorCodeRateLimitExceeded = -32002

	// ErrorCodeForwardingError is an implementation specific error code that
	// indicates that a http error occurred when forwarding a request to a
	// Darknode.
	ErrorCodeForwardingError = -32003

	// ErrorCodeTimeout is an implementation specific error code that indicates
	// that processing request takes more time than the given timeout.
	ErrorCodeTimeout = -32004
)

// Options are used when constructing a `Server`.
type Options struct {
	Port         string        // Server listens on
	MaxBatchSize int           // Maximum batch size that will be accepted
	Timeout      time.Duration // Timeout for each request
}

// SetZeroToDefault verify each field of the Options and set zero values to
// default.
func (options *Options) SetZeroToDefault() {
	if options.Port == "" {
		panic("port is not set in the options")
	}
	if options.MaxBatchSize == 0 {
		options.MaxBatchSize = 10
	}
	if options.Timeout == 0 {
		options.Timeout = 15 * time.Second
	}
}

// Server defines the HTTP server for the lightnode.
type Server struct {
	logger      logrus.FieldLogger
	options     Options
	rateLimiter RateLimiter
	validator   phi.Sender
}

// New constructs a new `Server` with the given options.
func New(logger logrus.FieldLogger, options Options, validator phi.Sender) *Server {
	rateLimiter := NewRateLimiter()
	options.SetZeroToDefault()
	return &Server{
		logger:      logger,
		options:     options,
		rateLimiter: rateLimiter,
		validator:   validator,
	}
}

// Listen starts the `Server` listening on its port. This function is blocking.
func (server *Server) Listen(ctx context.Context) {
	r := mux.NewRouter()
	r.HandleFunc("/health", server.healthCheck).Methods("GET")
	r.HandleFunc("/", server.handleFunc).Methods("POST")
	rm := NewRecoveryMiddleware(server.logger)
	r.Use(rm)

	httpHandler := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
		AllowedMethods:   []string{"POST"},
	}).Handler(r)

	// Init a new http server
	httpServer := http.Server{
		Addr:    fmt.Sprintf(":%s", server.options.Port),
		Handler: httpHandler,
	}
	// Close the server when ctx is canceled.
	go func() {
		<-ctx.Done()
		httpServer.Shutdown(ctx)
	}()

	// Start running the server.
	server.logger.Infof("lightnode listening on 0.0.0.0:%v...", server.options.Port)
	server.logger.Error(httpServer.ListenAndServe())
}

func (server *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	return
}

func (server *Server) handleFunc(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	darknodeID := v.Get("id")

	// Decode and validate request body in JSON.
	rawMessage := json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&rawMessage); err != nil {
		writeResponses(w, []jsonrpc.Response{errResponse(jsonrpc.ErrorCodeInvalidJSON, 0, "lightnode could not decode JSON request", nil)})
		return
	}

	// Unmarshal requests with support for batching
	reqs := []jsonrpc.Request{}
	if err := json.Unmarshal(rawMessage, &reqs); err != nil {
		// If we fail to unmarshal the raw message into a list of JSON-RPC 2.0
		// requests, try to unmarshal the raw message into a single JSON-RPC 2.0
		// request
		var req jsonrpc.Request
		if err := json.Unmarshal(rawMessage, &req); err != nil {
			writeResponses(w, []jsonrpc.Response{errResponse(jsonrpc.ErrorCodeInvalidJSON, 0, "lightnode could not parse JSON request", nil)})
			return
		}
		reqs = []jsonrpc.Request{req}
	}

	// Check that batch size does not exceed the maximum allowed batch size
	batchSize := len(reqs)
	if batchSize > server.options.MaxBatchSize {
		errMsg := fmt.Sprintf("maximum batch size exceeded: maximum is %v but got %v", server.options.MaxBatchSize, batchSize)
		writeResponses(w, []jsonrpc.Response{errResponse(ErrorCodeMaxBatchSizeExceeded, 0, errMsg, nil)})
		return
	}

	// Handle all requests concurrently and, after all responses have been
	// received, write all responses back to the http.ResponseWriter
	timer := time.After(server.options.Timeout)
	responses := make([]jsonrpc.Response, len(reqs))
	phi.ParForAll(reqs, func(i int) {
		method := reqs[i].Method

		// Ensure method exists prior to checking rate limit.
		_, ok := jsonrpc.RPCs[method]
		if !ok {
			responses[i] = errResponse(jsonrpc.ErrorCodeMethodNotFound, reqs[i].ID, "unsupported method", nil)
			return
		}

		// Try getting the host address without the port.
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if !server.rateLimiter.Allow(method, host) {
			responses[i] = errResponse(ErrorCodeRateLimitExceeded, reqs[i].ID, "rate limit exceeded", nil)
			return
		}

		// Send the request to validator and wait for response.
		reqWithResponder := NewRequestWithResponder(reqs[i], darknodeID)
		if ok := server.validator.Send(reqWithResponder); !ok {
			errMsg := "fail to send request to validator, too much back pressure in server"
			server.logger.Error(errMsg)
			responses[i] = errResponse(jsonrpc.ErrorCodeInternal, reqs[i].ID, errMsg, nil)
			return
		}
		select {
		case <-timer:
			responses[i] = errResponse(ErrorCodeTimeout, reqs[i].ID, fmt.Sprintf("timeout for request=%v, method= %v", reqs[i].ID, method), nil)
		case response := <-reqWithResponder.Responder:
			responses[i] = response
		case <-r.Context().Done():
			responses[i] = errResponse(ErrorCodeTimeout, reqs[i].ID, fmt.Sprintf("context canceled by the client for request=%v, method= %v", reqs[i].ID, method), nil)
		}
	})

	if err := writeResponses(w, responses); err != nil {
		server.logger.Errorf("error writing http response: %v", err)
	}
}

func errResponse(code int, id interface{}, message string, data json.RawMessage) jsonrpc.Response {
	err := jsonrpc.NewError(code, message, data)
	return jsonrpc.NewResponse(id, nil, &err)
}

func writeResponses(w http.ResponseWriter, responses []jsonrpc.Response) error {
	w.Header().Set("Content-Type", "application/json")
	if len(responses) == 1 {
		// We use the `writeError` function to determine the relevant status code
		// as we do not want to return a `http.StatusOK`.
		if responses[0].Error != nil {
			return writeError(w, responses[0].ID, *responses[0].Error)
		}
		return json.NewEncoder(w).Encode(responses[0])
	}
	return json.NewEncoder(w).Encode(responses)
}

func writeError(w http.ResponseWriter, id interface{}, err jsonrpc.Error) error {
	var statusCode int
	switch err.Code {
	case jsonrpc.ErrorCodeInvalidJSON, jsonrpc.ErrorCodeInvalidParams, jsonrpc.ErrorCodeInvalidRequest:
		statusCode = http.StatusBadRequest
	case jsonrpc.ErrorCodeMethodNotFound, jsonrpc.ErrorCodeResultNotFound:
		statusCode = http.StatusNotFound
	default:
		statusCode = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(jsonrpc.NewResponse(id, nil, &err))
}

// RequestWithResponder wraps a `jsonrpc.Request` with a responder channel that
// the response will be written to.
type RequestWithResponder struct {
	Request    jsonrpc.Request
	Responder  chan jsonrpc.Response
	DarknodeID string
}

// IsMessage implements the `phi.Message` interface.
func (RequestWithResponder) IsMessage() {}

// NewRequestWithResponder constructs a new request wrapper object.
func NewRequestWithResponder(req jsonrpc.Request, darknodeAddr string) RequestWithResponder {
	responder := make(chan jsonrpc.Response, 1)
	return RequestWithResponder{req, responder, darknodeAddr}
}