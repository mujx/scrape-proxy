package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// Configuration options.
var (
	httpAddr            = kingpin.Flag("web-url", "The endpoint to listen to for HTTP proxy requests.").Default(":8080").String()
	logLevel            = kingpin.Flag("log-level", "Minimum log level to use (debug, info, warn, error).").Default("info").String()
	registrationTimeout = kingpin.Flag("timeout", "The amount for which a client should be considered connected.").Default("30s").Duration()
	pollTimeout         = kingpin.Flag("poll-timeout", "The server will timeout clients waiting for a scrape request after this value.").Default("15s").Duration()
)

var (
	activeClientsGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "scrape_proxy_active_clients",
			Help: "The number of clients that are currently connected",
		},
	)

	httpAPICounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scrape_proxy_http_requests_total",
			Help: "Number of http api requests.",
		},
		[]string{"code", "path"},
	)

	httpProxyCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scrape_proxy_proxied_requests_total",
			Help: "Number of http proxy requests.",
		},
		[]string{"code"},
	)
)

type httpHandler struct {
	state *utils.GlobalState
	mux   http.Handler
	proxy http.Handler
}

type targetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

func (h *httpHandler) HandleListClients(w http.ResponseWriter, r *http.Request) {
	activeClients := h.state.GetClientList()

	targets := make([]*targetGroup, 0, len(activeClients))
	for _, v := range activeClients {
		targets = append(targets, &targetGroup{Targets: []string{v}, Labels: make(map[string]string)})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(targets)
}

func (h *httpHandler) HandleProxyRequests(w http.ResponseWriter, r *http.Request) {
	host := utils.ExtractHost(r)

	// Return 404 for unmanaged client IDs.
	if !h.state.IsClientAvailable(host) {
		log.WithFields(log.Fields{
			"clientId": host,
		}).Warn("Ignoring scrape request for un-registered client")

		w.WriteHeader(http.StatusNotFound)
		errorJson := map[string]string{"error": fmt.Sprintf("client '%s' is not managed", r.RequestURI)}
		json.NewEncoder(w).Encode(errorJson)

		return
	}

	clientResponseChannel := h.state.GetClientChannel(host)
	if clientResponseChannel == nil {
		log.WithFields(log.Fields{
			"clientId": host,
		}).Error("Scrape request for client with no channel to send the scrape results")
		errorJson := map[string]string{"error": fmt.Sprintf("client '%s' doesn't have a results channel", r.RequestURI)}

		w.WriteHeader(500)
		json.NewEncoder(w).Encode(errorJson)

		return
	}

	// Convert the raw HTTP request to a ProxyRequest for the client.
	var proxyReq utils.ProxyRequest
	proxyReq.ScrapeRequests = make(map[string]string)
	proxyReq.ScrapeRequests[host] = r.RequestURI

	log.WithFields(log.Fields{
		"clientId": host,
	}).Debug("Sending scrape request to client")

	// Forward the scrape response to the appropriate client through a channel.
	h.state.SendScrapeRequest(proxyReq, host)

	notify := w.(http.CloseNotifier).CloseNotify()

	var response utils.ProxyResponse

	select {
	// Wait for the response from the client.
	case response = <-clientResponseChannel:
		break
	case <-notify:
		log.WithFields(log.Fields{
			"clientId": host,
		}).Warn("Scrape request closed abruptly by the client")
		return
	case <-r.Context().Done():
		log.WithFields(log.Fields{
			"clientId": host,
		}).Warn("Scrape request is closed")
		return
	}

	if error, ok := response.Errors[host]; ok {
		log.WithFields(log.Fields{
			"clientId": host,
			"error":    error,
		}).Warn("Scrape request failed for client")

		w.WriteHeader(500)
		w.Header().Set("Content-Type", "application/json")
		errorJson := map[string]string{"error": error}
		json.NewEncoder(w).Encode(errorJson)

		return
	} else if payload, ok := response.Payload[host]; ok {
		log.WithFields(log.Fields{
			"clientId":    host,
			"payloadSize": len(payload),
		}).Debug("Scrape request succeeded")

		data := []byte(payload)

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		if _, err := w.Write(data); err != nil {
			log.WithFields(log.Fields{
				"clientId": host,
				"error":    string(err.Error()),
			}).Error("Failed to write response from client")
		}

		return
	}

	log.WithFields(log.Fields{
		"clientId": host,
	}).Error("No error or response was received from client")
	w.WriteHeader(500)
}

// ServeHTTP discriminates between proxy requests (e.g. from Prometheus) and other requests (e.g. from the Client).
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host != "" {
		h.proxy.ServeHTTP(w, r)
	} else {
		h.mux.ServeHTTP(w, r)
	}
}

func (h *httpHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var res utils.ProxyResponse
	err = json.Unmarshal(body, &res)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	clientChannel := h.state.GetClientChannel(res.Id)

	if errValue, ok := res.Errors[res.Id]; ok && clientChannel != nil {
		// The client returned an error.
		clientChannel <- res

		log.WithFields(log.Fields{
			"clientId": res.Id,
			"err":      errValue,
		}).Debug("Client returned with an error")
	} else if _, ok := res.Payload[res.Id]; ok && clientChannel != nil {
		// The client returned succefully.
		clientChannel <- res

		log.WithFields(log.Fields{
			"clientId": res.Id,
		}).Debug("Client returned succefully")
	} else {
		// It's a heartbeat.
		h.state.AddClient(res.Id)

		// Update the Prometheus metric.
		activeClientsGauge.Set(float64(len(h.state.GetClientList())))

		log.WithFields(log.Fields{
			"clientId": res.Id,
		}).Debug("Received heartbeat")
	}

	w.WriteHeader(200)
}

func (h *httpHandler) HandlePull(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var pullRequest utils.PullRequest
	err = json.Unmarshal(body, &pullRequest)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Return 404 for unmanaged client IDs.
	if !h.state.IsClientAvailable(pullRequest.Id) {
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
		}).Warn("Pull request from un-registered client")

		w.WriteHeader(http.StatusNotFound)
		errorJson := map[string]string{"error": fmt.Sprintf("client '%s' is not managed", pullRequest.Id)}
		json.NewEncoder(w).Encode(errorJson)
		return
	}

	notify := w.(http.CloseNotifier).CloseNotify()

	log.WithFields(log.Fields{
		"clientId": pullRequest.Id,
	}).Debug("Received pull request")

	clientChannel := h.state.GetIncomingRequestsChannel(pullRequest.Id)

	if clientChannel == nil {
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
		}).Error("Registered client with nil channel for incoming requests")
		w.WriteHeader(500)

		return
	}

	log.WithFields(log.Fields{
		"clientId": pullRequest.Id,
	}).Debug("Client is waiting for a scrape request")

	select {
	case req := <-clientChannel:
		w.WriteHeader(200)
		w.Header().Set("Content-Type", "application/json")
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
			"request":  req,
		}).Debug("Scrape request was sent to the client")

		json.NewEncoder(w).Encode(req)
		return
	case <-notify:
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
		}).Error("Connection closed abruptly by the client")
		return
	case <-time.After((*pollTimeout)):
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
		}).Debug("Timeout reached. Closing connection")

		// Timeout.
		w.WriteHeader(504)
		return
	}
}

func newHTTPHandler(globalState *utils.GlobalState) *httpHandler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	h := &httpHandler{mux: mux, state: globalState}

	handlers := map[string]http.HandlerFunc{
		"/push":    h.HandlePush,
		"/pull":    h.HandlePull,
		"/clients": h.HandleListClients,
	}

	for path, handlerFunc := range handlers {
		counter := httpAPICounter.MustCurryWith(prometheus.Labels{"path": path})
		mux.Handle(path, promhttp.InstrumentHandlerCounter(counter, http.HandlerFunc(handlerFunc)))
		counter.WithLabelValues("200")
	}

	h.proxy = promhttp.InstrumentHandlerCounter(httpProxyCounter, http.HandlerFunc(h.HandleProxyRequests))

	return h
}

func main() {
	kingpin.Parse()

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		log.WithFields(log.Fields{
			"err": string(err.Error()),
		}).Error("failed to parse log level")
		os.Exit(1)
	}

	utils.InitLogger(level)

	var globalState utils.GlobalState
	globalState.Init(*registrationTimeout)

	go func() {
		for range time.Tick(time.Minute) {
			globalState.CleanUpOldClients()

			// Update the Prometheus metric.
			activeClientsGauge.Set(float64(len(globalState.GetClientList())))
		}
	}()

	log.WithFields(log.Fields{
		"httpAddr":            *httpAddr,
		"logLevel":            *logLevel,
		"registrationTimeout": *registrationTimeout,
		"pollTimeout":         *pollTimeout,
	}).Info("scrape-proxy server started")

	if err := http.ListenAndServe(*httpAddr, newHTTPHandler(&globalState)); err != nil {
		log.WithFields(log.Fields{"error": string(err.Error())}).Error("failed to setup HTTP server")
		os.Exit(1)
	}
}
