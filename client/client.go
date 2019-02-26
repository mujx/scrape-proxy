package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/satori/go.uuid"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// CLI configuration options.
var (
	proxyUrl          = kingpin.Flag("proxy-url", "The proxy endpoint.").Default("http://localhost:8080").String()
	remoteFQDN        = kingpin.Flag("remote-fqdn", "FQDN to forward the scrape requests.").Default("localhost").String()
	logLevel          = kingpin.Flag("log-level", "Minimum log level to use (trace, debug, info, warn, error).").Default("info").String()
	heartbeatInterval = kingpin.Flag("heartbeat", "The heartbeat duration.").Default("10s").Duration()
)

func StartHeartBeat(clientName string, proxyUrl string, interval time.Duration) {
	for {
		log.WithFields(log.Fields{"clientName": clientName}).Debug("Initiating heartbeat")

		var heartbeat utils.SurveyResponse
		heartbeat.Id = clientName

		body, err := json.Marshal(heartbeat)
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
				"err": string(err.Error()),
			}).Error("Failed to serialize heartbeat")
			continue
		}

		_, err = http.Post(proxyUrl+"/push", "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.WithFields(log.Fields{
				"clientName": clientName,
				"err":        string(err.Error()),
			}).Error("Failed to send heartbeat")
			continue
		}

		time.Sleep(interval)
	}
}

func SendScrapeResults(clientName string, proxyUrl string, responseChannel chan utils.SurveyResponse) {
	for {
		// We wait until a scrape request has been executed so we can
		// send the result back to the server.
		response := <-responseChannel

		body, err := json.Marshal(response)
		if err != nil {
			log.WithFields(log.Fields{
				"msg":        string(err.Error()),
				"clientName": clientName,
			}).Error("failed to serialize scrape response")
			continue
		}

		_, err = http.Post(proxyUrl+"/push", "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.WithFields(log.Fields{
				"clientName": clientName,
				"err":        string(err.Error()),
			}).Error("Failed to send scrape results")
			continue
		}
	}
}

func WaitForScrapeRequests(clientName string, proxyUrl string, remoteFQDN string, responseChannel chan utils.SurveyResponse) {
	for {
		var req utils.PullRequest
		req.Id = clientName

		body, err := json.Marshal(req)
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
				"err": string(err.Error()),
			}).Error("Failed to serialize heartbeat")
			time.Sleep(time.Duration(time.Second))

			continue
		}

		resp, err := http.Post(proxyUrl+"/pull", "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.WithFields(log.Fields{
				"id":  clientName,
				"err": string(err.Error()),
			}).Error("Failed to receive any scrape requests")
			time.Sleep(time.Duration(time.Second))
			continue
		}

		if resp.StatusCode == 404 || resp.StatusCode >= 500 {
			time.Sleep(time.Duration(time.Second))
			log.WithFields(log.Fields{
				"statusCode": resp.StatusCode,
			}).Debug("No scrape requests received")
			continue
		}

		b, err := ioutil.ReadAll(resp.Body)
		defer resp.Body.Close()
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
			}).Warning("failed to read response body")
			continue
		}

		var request utils.SurveyRequest
		if err := json.Unmarshal(b, &request); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
				"body":  string(b),
			}).Error("failed to parse scrape request")
			continue
		}

		go DoScrape(clientName, request, remoteFQDN, responseChannel)
	}
}

func DoScrape(name string, request utils.SurveyRequest, remoteFQDN string, responseChannel chan utils.SurveyResponse) {
	var response utils.SurveyResponse
	response.Id = name
	response.Errors = make(map[string]string)
	response.Payload = make(map[string]string)

	remoteURI, ok := request.ScrapeRequests[name]

	if ok {
		uri := strings.Replace(remoteURI, response.Id, remoteFQDN, -1)

		log.WithFields(
			log.Fields{
				"uri":        request,
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
		}).Error("failed to parse log level")
		os.Exit(1)
	}
	utils.InitLogger(level)

	clientName := uuid.NewV4()

	responseChannel := make(chan utils.SurveyResponse, 256)

	log.WithFields(log.Fields{
		"proxyUrl":   *proxyUrl,
		"clientName": clientName,
	}).Info("scrape-proxy client started")

	go StartHeartBeat(clientName.String(), *proxyUrl, *heartbeatInterval)
	go SendScrapeResults(clientName.String(), *proxyUrl, responseChannel)

	WaitForScrapeRequests(clientName.String(), *proxyUrl, *remoteFQDN, responseChannel)
}
