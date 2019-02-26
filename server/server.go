package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"nanomsg.org/go/mangos/v2"
	"nanomsg.org/go/mangos/v2/protocol/pub"
	"nanomsg.org/go/mangos/v2/protocol/rep"
	_ "nanomsg.org/go/mangos/v2/transport/all"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// Configuration options.
var (
	pushUrl             = kingpin.Flag("push-url", "The endpoint to public scrape requests.").Default("tcp://*:5050").String()
	pullUrl             = kingpin.Flag("pull-url", "The endpoint to to receive scrape results.").Default("tcp://*:5051").String()
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

func SendScrapeRequests(state *utils.GlobalState, pushUrl string) {
	var sock mangos.Socket
	var err error

	if sock, err = pub.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"err": string(err.Error()),
		}).Error("Failed to create new PUB socket")
		return
	}

	if err = sock.Listen(pushUrl); err != nil {
		log.WithFields(log.Fields{
			"pushUrl": pushUrl,
			"err":     string(err.Error()),
		}).Error("Failed to listen on PUB socket")
		return
	}

	for {
		req := state.GetNextScrapeRequest()

		var clientId = utils.ExtractHost(&req)

		log.WithFields(log.Fields{
			"clientId": clientId,
		}).Debug("Forwarding scrape request")

		var surveyReq utils.SurveyRequest
		surveyReq.ScrapeRequests = make(map[string]string)
		surveyReq.ScrapeRequests[clientId] = req.RequestURI

		val, err := json.Marshal(surveyReq)
		if err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("failed to serialize PUB request")
			continue
		}

		var sb strings.Builder
		sb.WriteString(clientId)
		sb.WriteString(" ")
		sb.WriteString(string(val))

		if err = sock.Send([]byte(sb.String())); err != nil {
			log.WithFields(log.Fields{
				"err": string(err.Error()),
			}).Error("Failed to send on PUB socket")
			return
		}
	}

}

func HandleScrapeResponses(globalState *utils.GlobalState, pullUrl string) {
	var sock mangos.Socket
	var err error
	var msg []byte

	if sock, err = rep.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"err": string(err.Error()),
		}).Error("Failed to create new REP socket")
		return
	}

	if err = sock.Listen(pullUrl); err != nil {
		log.WithFields(log.Fields{
			"err": string(err.Error()),
		}).Error("Failed to listen on REP socket")
		return
	}

	for {
		msg, err = sock.Recv()
		if err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("Failed to receive REQ")
			return
		}

		var response utils.SurveyResponse
		if err := json.Unmarshal(msg, &response); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("Failed to parse scrape response")
			continue
		}

		// This channel will be used to communicate back to
		// the http proxy handler the results of the scrape request.
		clientChannel := globalState.GetClientChannel(response.Id)

		if errValue, ok := response.Errors[response.Id]; ok {
			// The client returned an error.
			clientChannel <- response

			log.WithFields(log.Fields{
				"clientId": response.Id,
				"err":      errValue,
			}).Debug("Client returned with an error")
		} else if _, ok := response.Payload[response.Id]; ok {
			// The client returned succefully.
			clientChannel <- response

			log.WithFields(log.Fields{
				"clientId": response.Id,
			}).Debug("Client returned succefully")
		} else {
			// It's a heartbeat.
			globalState.AddClient(response.Id)

			log.WithFields(log.Fields{
				"clientId": response.Id,
			}).Debug("Received heartbeat")
		}

		err = sock.Send([]byte("OK"))
		if err != nil {
			log.WithFields(log.Fields{
				"id": response.Id,
			}).Error("Failed to ACK request from client")
		}
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
	for _, v := range activeClients {
		targets = append(targets, &targetGroup{Targets: []string{v}, Labels: make(map[string]string)})
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

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		log.WithFields(log.Fields{
			"err": string(err.Error()),
		}).Info("failed to parse log level")
		os.Exit(1)
	}

	utils.InitLogger(level)

	var globalState utils.GlobalState
	globalState.Init(*registrationTimeout)

	go func() {
		for {
			SendScrapeRequests(&globalState, *pushUrl)
			time.Sleep(time.Duration(time.Second) * time.Duration(utils.RetryInterval))
		}
	}()

	go func() {
		for {
			HandleScrapeResponses(&globalState, *pullUrl)
			time.Sleep(time.Duration(time.Second) * time.Duration(utils.RetryInterval))
		}
	}()

	log.WithFields(log.Fields{
		"pushUrl":  *pushUrl,
		"pullUrl":  *pullUrl,
		"httpAddr": *httpAddr,
	}).Info("scrape-proxy server started")

	if err := http.ListenAndServe(*httpAddr, newHTTPHandler(&globalState)); err != nil {
		log.WithFields(log.Fields{"error": string(err.Error())}).Error("failed to setup HTTP server")
		os.Exit(1)
	}
}
