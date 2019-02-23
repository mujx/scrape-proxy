package utils

import (
	"net/http"
	"os"
	"strings"
	"sync"

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
	ClientList map[string]bool
	// The HTTP server waits on those channels for a scrape response from
	// each connect client.
	ClientChannels map[string]chan SurveyResponse

	mutex sync.Mutex
}

func (state *GlobalState) Init() {
	state.IncomingScrapes = make(chan http.Request, 20)
	state.ClientChannels = map[string]chan SurveyResponse{}
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

func (state *GlobalState) SetClientList(clientList map[string]bool) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for clientId, _ := range clientList {
		if _, ok := state.ClientChannels[clientId]; !ok {
			state.ClientChannels[clientId] = make(chan SurveyResponse, 100)
		}
	}

	// TODO: Delete client channels not found in the list.

	state.ClientList = clientList
}

func (state *GlobalState) GetClientList() map[string]bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	return state.ClientList
}

func (state *GlobalState) GetNextScrapeRequest() *http.Request {
	select {
	case req, ok := <-state.IncomingScrapes:
		if !ok {
			return nil
		}

		return &req
	default:
	}

	return nil
}

func ExtractHost(req *http.Request) string {
	return strings.Split(req.Host, ":")[0]
}
