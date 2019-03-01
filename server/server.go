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
	"github.com/prometheus/client_golang/prometheus/promhttp"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// Configuration options.
var (
	httpAddr            = kingpin.Flag("web-url", "The endpoint to listen to for HTTP proxy requests.").Default(":8080").String()
	logLevel            = kingpin.Flag("log-level", "Minimum log level to use (trace, debug, info, warn, error).").Default("info").String()
	registrationTimeout = kingpin.Flag("timeout", "The amount for which a client should be considered connected.").Default("30s").Duration()
)

var (
	httpAPICounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushprox_http_requests_total",
			Help: "Number of http api requests.",
		},
		[]string{"code", "path"},
	)

	httpProxyCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushproxy_proxied_requests_total",
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
		w.WriteHeader(http.StatusNotFound)
		errorJson := map[string]string{"error": fmt.Sprintf("client '%s' is not managed", r.RequestURI)}
		json.NewEncoder(w).Encode(errorJson)

		return
	}

	clientChannel := h.state.GetClientChannel(host)

	// Convert the raw HTTP request to a ProxyRequest for the client.
	var proxyReq utils.ProxyRequest
	proxyReq.ScrapeRequests = make(map[string]string)
	proxyReq.ScrapeRequests[host] = r.RequestURI

	h.state.SendScrapeRequest(proxyReq, host)

	response := <-clientChannel

	if error, ok := response.Errors[host]; ok {
		w.WriteHeader(500)
		w.Header().Set("Content-Type", "application/json")

		errorJson := map[string]string{"error": error}
		json.NewEncoder(w).Encode(errorJson)
	} else if payload, ok := response.Payload[host]; ok {
		w.Write([]byte(payload))
	} else {
		w.WriteHeader(500)
	}
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

	if errValue, ok := res.Errors[res.Id]; ok {
		// The client returned an error.
		clientChannel <- res

		log.WithFields(log.Fields{
			"clientId": res.Id,
			"err":      errValue,
		}).Debug("Client returned with an error")
	} else if _, ok := res.Payload[res.Id]; ok {
		// The client returned succefully.
		clientChannel <- res

		log.WithFields(log.Fields{
			"clientId": res.Id,
		}).Debug("Client returned succefully")
	} else {
		// It's a heartbeat.
		h.state.AddClient(res.Id)

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

	select {
	case req := <-clientChannel:
		w.WriteHeader(200)
		w.Header().Set("Content-Type", "application/json")

		json.NewEncoder(w).Encode(req)
		return
	case <-notify:
		log.WithFields(log.Fields{
			"clientId": pullRequest.Id,
		}).Error("Connection closed abruptly")
		return
	case <-time.After(15 * time.Second):
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

	log.WithFields(log.Fields{"httpAddr": *httpAddr}).Info("scrape-proxy server started")

	if err := http.ListenAndServe(*httpAddr, newHTTPHandler(&globalState)); err != nil {
		log.WithFields(log.Fields{"error": string(err.Error())}).Error("failed to setup HTTP server")
		os.Exit(1)
	}
}
