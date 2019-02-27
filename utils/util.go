package utils

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var RetryInterval = 5

func InitLogger(level log.Level) {
	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: false,
		FieldMap: log.FieldMap{
			log.FieldKeyLevel: "level",
			log.FieldKeyTime:  "ts",
			log.FieldKeyMsg:   "msg",
		}})
	log.SetOutput(os.Stdout)
	log.SetLevel(level)
}

// The clients will send this payload to start waiting for incoming scrape requests.
type PullRequest struct {
	Id string `json:"id"`
}

// The server will send back a proxy request.
type ProxyRequest struct {
	ScrapeRequests map[string]string `json:"scrape_requests"`
}

// The client's response to a scrape request containing the scrape response
// and any potential errors.
type ProxyResponse struct {
	Id      string            `json:"id"`
	Payload map[string]string `json:"payload"`
	Errors  map[string]string `json:"errors"`
}

type GlobalState struct {
	// The HTTP server uses these channels to send to each client
	// new scrape requests.
	incomingScrapes map[string]chan ProxyRequest
	// The list of clients that appear to be active.
	clientList map[string]time.Time
	// The HTTP server waits on those channels for a scrape response from
	// each connect client.
	clientChannels map[string]chan ProxyResponse

	registrationTimeout time.Duration
	mutex               sync.Mutex
}

func (state *GlobalState) Init(registrationTimeout time.Duration) {
	state.incomingScrapes = map[string]chan ProxyRequest{}
	state.clientChannels = map[string]chan ProxyResponse{}

	state.clientList = map[string]time.Time{}
	state.registrationTimeout = registrationTimeout

	go func() {
		for range time.Tick(1 * time.Minute) {
			state.cleanUpOldClients()
		}
	}()
}

func (state *GlobalState) cleanUpOldClients() {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	// We pick a high enough timeout so the channels will not be used.
	// After a client is no longer in the list, all incoming requests for
	// it are dropped so the channels is unused.
	limit := time.Now().Add(-state.registrationTimeout * 5)
	deletedClients := 0
	deletedOutChannels := 0
	deletedInChannels := 0

	for k, ts := range state.clientList {
		if ts.Before(limit) {
			delete(state.clientList, k)
			deletedClients++

			if _, ok := state.clientChannels[k]; ok {
				delete(state.clientChannels, k)
				deletedOutChannels++
			}

			if _, ok := state.incomingScrapes[k]; ok {
				delete(state.incomingScrapes, k)
				deletedInChannels++
			}
		}
	}

	log.WithFields(log.Fields{
		"deletedClients":       deletedClients,
		"remainingClients":     len(state.clientList),
		"deletedOutChannels":   deletedOutChannels,
		"deletedInChannels":    deletedInChannels,
		"remainingOutChannels": len(state.clientChannels),
		"remainingInChannels":  len(state.incomingScrapes),
	}).Info("Removed old clients & channels")
}

func (state *GlobalState) SendScrapeRequest(req ProxyRequest, clientId string) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	if _, ok := state.incomingScrapes[clientId]; !ok {
		state.incomingScrapes[clientId] = make(chan ProxyRequest, 256)
	}

	state.incomingScrapes[clientId] <- req
}

func (state *GlobalState) IsClientAvailable(id string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	t, ok := state.clientList[id]

	if ok {
		limit := time.Now().Add(-state.registrationTimeout)
		return limit.Before(t)
	}

	return false
}

func (state *GlobalState) GetClientChannel(id string) chan ProxyResponse {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	return state.clientChannels[id]
}

func (state *GlobalState) AddClient(clientId string) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	if _, ok := state.clientChannels[clientId]; !ok {
		state.clientChannels[clientId] = make(chan ProxyResponse, 256)
	}

	if _, ok := state.incomingScrapes[clientId]; !ok {
		state.incomingScrapes[clientId] = make(chan ProxyRequest, 256)
	}

	state.clientList[clientId] = time.Now()
}

func (state *GlobalState) GetClientList() []string {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	limit := time.Now().Add(-state.registrationTimeout)
	known := make([]string, 0, len(state.clientList))

	for k, t := range state.clientList {
		if limit.Before(t) {
			known = append(known, k)
		}
	}

	return known
}

func (state *GlobalState) GetIncomingRequestsChannel(clientId string) chan ProxyRequest {
	return state.incomingScrapes[clientId]
}

func ExtractHost(req *http.Request) string {
	return strings.Split(req.Host, ":")[0]
}
