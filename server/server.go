package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"nanomsg.org/go/mangos/v2"
	"nanomsg.org/go/mangos/v2/protocol/surveyor"
	_ "nanomsg.org/go/mangos/v2/transport/all"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// Configuration options.
var (
	surveyTimeout = kingpin.Flag("survey.timeout", "The maximum number of seconds to wait for a response.").Default("10").Int()
	listenAddr    = kingpin.Flag("client.listen-address", "The endpoint to listen for clients.").Default("tcp://*:5050").String()
	httpAddr      = kingpin.Flag("web.listen-address", "Address to listen on for HTTP proxy requests.").Default(":8080").String()
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

func ServiceDiscovery(globalState *utils.GlobalState, url string, timeout int) {
	var sock mangos.Socket
	var err error
	var msg []byte

	if sock, err = surveyor.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"error": string(err.Error()),
		}).Error("cannot setup new surveyor socket")
		return
	}

	if err = sock.Listen(url); err != nil {
		log.WithFields(log.Fields{
			"error": string(err.Error()),
		}).Error("cannot listen on surveyor socket")
		return
	}

	err = sock.SetOption(mangos.OptionSurveyTime, time.Duration(timeout)*time.Second)

	if err != nil {
		log.WithFields(log.Fields{
			"error": string(err.Error()),
		}).Error("cannot set options on socket")
		return
	}

	for {
		req := globalState.GetNextScrapeRequest()

		var surveyReq utils.SurveyRequest
		surveyReq.ScrapeRequests = make(map[string]string)

		if req != nil {
			surveyReq.ScrapeRequests[utils.ExtractHost(req)] = req.RequestURI
		}

		val, err := json.Marshal(surveyReq)
		if err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("failed to serialize request")
			continue
		}

		if err = sock.Send([]byte(val)); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("failed to send request")
			continue
		}

		var activeClients = make(map[string]bool)

		// Waiting for all the clients to respond, or until
		// the OptionSurveyTime (maximum time to wait for a response) is reached.
		for {
			if msg, err = sock.Recv(); err != nil {
				break
			}

			var response utils.SurveyResponse

			if err := json.Unmarshal(msg, &response); err != nil {
				log.WithFields(log.Fields{
					"error": string(err.Error()),
				}).Error("failed to parse survey response")
				continue
			}

			activeClients[response.Id] = true

			// This channel will be used to communicate back to
			// the http proxy handler the results of the scrape request.
			clientChannel := globalState.GetClientChannel(response.Id)

			if _, ok := response.Errors[response.Id]; ok {
				// The client returned an error.
				clientChannel <- response
			} else if _, ok := response.Payload[response.Id]; ok {
				// The client returned succefully.
				clientChannel <- response
			}
		}

		globalState.SetClientList(activeClients)
	}
}

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
	for id, _ := range activeClients {
		targets = append(targets, &targetGroup{Targets: []string{id}, Labels: make(map[string]string)})
	}

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

	h.state.SendScrapeRequest(*r)

	response := <-clientChannel

	if error, ok := response.Errors[host]; ok {
		w.WriteHeader(500)
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

func newHTTPHandler(globalState *utils.GlobalState) *httpHandler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	h := &httpHandler{mux: mux, state: globalState}
	clientsPath := "/clients"

	counter := httpAPICounter.MustCurryWith(prometheus.Labels{"path": clientsPath})
	mux.Handle(clientsPath, promhttp.InstrumentHandlerCounter(counter, http.HandlerFunc(h.HandleListClients)))
	counter.WithLabelValues("200")

	h.proxy = promhttp.InstrumentHandlerCounter(httpProxyCounter, http.HandlerFunc(h.HandleProxyRequests))

	return h
}

func main() {
	kingpin.Parse()

	utils.InitLogger()

	var globalState utils.GlobalState
	globalState.Init()

	go func() {
		for {
			ServiceDiscovery(&globalState, *listenAddr, *surveyTimeout)
			time.Sleep(time.Second)
		}
	}()

	log.WithFields(log.Fields{
		"surveyTimeout":    *surveyTimeout,
		"clientListenAddr": *listenAddr,
		"httpAddr":         *httpAddr,
	}).Info("scrape-proxy server started")

	if err := http.ListenAndServe(*httpAddr, newHTTPHandler(&globalState)); err != nil {
		log.WithFields(log.Fields{"error": string(err.Error())}).Error("failed to setup HTTP server")
		os.Exit(1)
	}
}
