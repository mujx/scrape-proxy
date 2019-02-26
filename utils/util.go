package utils

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

func InitLogger() {
	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
}

type SurveyRequest struct {
	ScrapeRequests map[string]string `json:"scrape_requests"`
}

type SurveyResponse struct {
	Id      string            `json:"id"`
	Payload map[string]string `json:"payload"`
	Errors  map[string]string `json:"errors"`
}

type GlobalState struct {
	// The HTTP server uses the channel to communicate with the service
	// discovery mechanism for new alerts.
	IncomingScrapes chan http.Request
	// The list of clients that have been respond to the latest service
	// discovery query.
	ClientList map[string]time.Time
	// The HTTP server waits on those channels for a scrape response from
	// each connect client.
	ClientChannels map[string]chan SurveyResponse

	registrationTimeout time.Duration
	mutex               sync.Mutex
}

func (state *GlobalState) Init(registrationTimeout time.Duration) {
	state.IncomingScrapes = make(chan http.Request, 20)
	state.ClientChannels = map[string]chan SurveyResponse{}
	state.ClientList = map[string]time.Time{}
	state.registrationTimeout = registrationTimeout
}

func (state *GlobalState) cleanUpOldClients() {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	limit := time.Now().Add(-state.registrationTimeout)
	deleted := 0

	// TODO: Clean up old channels also.

	for k, ts := range state.ClientList {
		if ts.Before(limit) {
			delete(state.ClientList, k)
			deleted++
		}
	}
}

func (state *GlobalState) SendScrapeRequest(req http.Request) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	state.IncomingScrapes <- req
}

func (state *GlobalState) IsClientAvailable(id string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	_, ok := state.ClientList[id]

	return ok
}

func (state *GlobalState) GetClientChannel(id string) chan SurveyResponse {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	return state.ClientChannels[id]
}

func (state *GlobalState) DeleteClientChannel(id string) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	ch, ok := state.ClientChannels[id]

	if ok {
		close(ch)
		delete(state.ClientChannels, id)
	}
}

func (state *GlobalState) AddClient(clientId string) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	if _, ok := state.ClientChannels[clientId]; !ok {
		state.ClientChannels[clientId] = make(chan SurveyResponse, 100)
	}

	state.ClientList[clientId] = time.Now()
}

func (state *GlobalState) GetClientList() []string {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	limit := time.Now().Add(-state.registrationTimeout)
	known := make([]string, 0, len(state.ClientList))

	for k, t := range state.ClientList {
		if limit.Before(t) {
			known = append(known, k)
		}
	}

	return known
}

func (state *GlobalState) GetNextScrapeRequest() http.Request {
	return <-state.IncomingScrapes
}

func ExtractHost(req *http.Request) string {
	return strings.Split(req.Host, ":")[0]
}
