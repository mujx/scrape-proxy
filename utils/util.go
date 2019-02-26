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
	deletedChannels := 0

	for k, ts := range state.ClientList {
		if ts.Before(limit) {
			delete(state.ClientList, k)
			deletedClients++

			if _, ok := state.ClientChannels[k]; ok {
				delete(state.ClientChannels, k)
				deletedChannels++
			}
		}
	}

	log.WithFields(log.Fields{
		"deletedClients":    deletedClients,
		"remainingClients":  len(state.ClientList),
		"deletedChannels":   deletedChannels,
		"remainingChannels": len(state.ClientChannels),
	}).Info("Removed old clients & channels")
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
