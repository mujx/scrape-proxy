package main

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"nanomsg.org/go/mangos/v2"
	"nanomsg.org/go/mangos/v2/protocol/req"
	"nanomsg.org/go/mangos/v2/protocol/sub"
	_ "nanomsg.org/go/mangos/v2/transport/all"

	"github.com/satori/go.uuid"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// CLI configuration options.
var (
	pullUrl           = kingpin.Flag("pull-url", "The endpoint to listen for scrape requests.").Default("tcp://127.0.0.1:5050").String()
	pushUrl           = kingpin.Flag("push-url", "The endpoint to send scrape responses & heartbeat.").Default("tcp://127.0.0.1:5051").String()
	remoteFQDN        = kingpin.Flag("remote-fqdn", "FQDN to forward the scrape requests.").Default("localhost").String()
	logLevel          = kingpin.Flag("log-level", "Minimum log level to use (trace, debug, info, warn, error).").Default("info").String()
	heartbeatInterval = kingpin.Flag("heartbeat", "The heartbeat duration.").Default("10s").Duration()
)

func StartHeartBeat(clientName string, pushUrl string, interval time.Duration) {
	var sock mangos.Socket
	var err error

	if sock, err = req.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"err":        string(err.Error()),
		}).Error("Failed to create REQ socket")
		return
	}

	if err = sock.Dial(pushUrl); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"url":        pushUrl,
			"err":        string(err.Error()),
		}).Error("Failed to connect to REP socket")
		return
	}

	for {
		time.Sleep(interval)
		log.WithFields(log.Fields{"clientName": clientName}).Debug("Initiating heartbeat")

		var heartbeat utils.SurveyResponse
		heartbeat.Id = clientName

		value, err := json.Marshal(heartbeat)
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
				"err": string(err.Error()),
			}).Error("Failed to serialize heartbeat")
			break
		}

		if err = sock.Send(value); err != nil {
			log.WithFields(log.Fields{
				"clientName": clientName,
				"err":        string(err.Error()),
			}).Error("Failed to send heartbeat")
			break
		}

		if _, err = sock.Recv(); err != nil {
			log.WithFields(log.Fields{
				"pullUrl": pullUrl,
				"err":     string(err.Error()),
			}).Error("Failed to receive ack on heartbeat")
			break
		}
	}

	sock.Close()
}

func SendScrapeResults(clientName string, pushUrl string, responseChannel chan utils.SurveyResponse) {
	var sock mangos.Socket
	var err error

	if sock, err = req.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"err":        string(err.Error()),
		}).Error("Failed to create REQ socket")
		return
	}

	if err = sock.Dial(pushUrl); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"url":        pushUrl,
			"err":        string(err.Error()),
		}).Error("Failed to connect to REP socket")
		return
	}

	for {
		// We wait until a scrape request has been executed so we can
		// send the result back to the server.
		response := <-responseChannel

		value, err := json.Marshal(response)
		if err != nil {
			log.WithFields(log.Fields{
				"msg":        string(err.Error()),
				"clientName": clientName,
			}).Error("failed to serialize scrape response")
			return
		}

		if err = sock.Send(value); err != nil {
			log.WithFields(log.Fields{
				"clientName": clientName,
				"err":        string(err.Error()),
			}).Error("Failed to send scrape results")
			break
		}

		if _, err = sock.Recv(); err != nil {
			log.WithFields(log.Fields{
				"pullUrl": *pullUrl,
				"err":     string(err.Error()),
			}).Error("Failed to receive response")
			break
		}
	}

	sock.Close()
}

func WaitForScrapeRequests(clientName string, pullUrl string, remoteFQDN string, responseChannel chan utils.SurveyResponse) {
	var sock mangos.Socket
	var err error
	var msg []byte

	if sock, err = sub.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"err":        string(err.Error()),
		}).Error("Failed to create new SUB socket")
		return
	}

	if err = sock.Dial(pullUrl); err != nil {
		log.WithFields(log.Fields{
			"clientName": clientName,
			"url":        pushUrl,
			"err":        string(err.Error()),
		}).Error("Failed to connect to PUB socket")
		return
	}

	// Subscribe to events that are only meant for this client.
	err = sock.SetOption(mangos.OptionSubscribe, []byte(clientName))

	if err != nil {
		log.WithFields(log.Fields{
			"topic": clientName,
			"err":   string(err.Error()),
		}).Error("Failed to subscripe")
		return
	}

	for {
		if msg, err = sock.Recv(); err != nil {
			log.WithFields(log.Fields{
				"topic": clientName,
				"err":   string(err.Error()),
			}).Error("Failed to receive")
			return
		}

		parts := strings.SplitN(string(msg), " ", 2)

		log.WithFields(
			log.Fields{
				"payload":    string(msg),
				"clientName": clientName,
			}).Debug("Scrape request received")

		if len(parts) != 2 {
			log.WithFields(log.Fields{
				"msg": string(msg),
				"err": string(err.Error()),
			}).Error("Failed to parse the header of the message")
			continue
		}

		// Remove the header of the message.
		header, body := parts[0], parts[1]

		if header != clientName {
			log.WithFields(log.Fields{
				"header":     header,
				"clientName": clientName,
			}).Error("Received scrape request for another client")
			continue
		}

		go DoScrape(clientName, []byte(body), remoteFQDN, responseChannel)
	}
}

func DoScrape(name string, msg []byte, remoteFQDN string, responseChannel chan utils.SurveyResponse) {
	var request utils.SurveyRequest
	if err := json.Unmarshal(msg, &request); err != nil {
		log.WithFields(log.Fields{
			"error": string(err.Error()),
		}).Error("failed to parse scrape request")
		return
	}

	var response utils.SurveyResponse
	response.Id = name
	response.Errors = make(map[string]string)
	response.Payload = make(map[string]string)

	remoteURI, ok := request.ScrapeRequests[name]

	if ok {
		uri := strings.Replace(remoteURI, response.Id, remoteFQDN, -1)

		log.WithFields(
			log.Fields{
				"uri":        string(msg),
				"clientName": name,
			}).Debug("Performing scrape request")

		resp, err := http.Get(uri)
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
				"uri": uri,
			}).Warning("scrape request failed")

			response.Errors[response.Id] = string(err.Error())
		} else {
			body, err := ioutil.ReadAll(resp.Body)
			defer resp.Body.Close()

			if err != nil {
				log.WithFields(log.Fields{
					"msg": string(err.Error()),
					"uri": remoteURI,
				}).Warning("failed to read response body")

				response.Errors[response.Id] = string(err.Error())
			} else {
				response.Payload[response.Id] = string(body)
			}
		}

		responseChannel <- response
	}
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

	clientName := uuid.NewV4()

	responseChannel := make(chan utils.SurveyResponse, 256)

	log.WithFields(log.Fields{
		"pullUrl":    *pullUrl,
		"pushUrl":    *pushUrl,
		"clientName": clientName,
	}).Info("scrape-proxy client started")

	go func() {
		for {
			StartHeartBeat(clientName.String(), *pushUrl, *heartbeatInterval)
			time.Sleep(time.Duration(time.Second) * time.Duration(utils.RetryInterval))
		}
	}()

	go func() {
		for {
			SendScrapeResults(clientName.String(), *pushUrl, responseChannel)
			time.Sleep(time.Duration(time.Second) * time.Duration(utils.RetryInterval))
		}
	}()

	for {
		WaitForScrapeRequests(clientName.String(), *pullUrl, *remoteFQDN, responseChannel)
		time.Sleep(time.Duration(time.Second) * time.Duration(utils.RetryInterval))
	}
}
