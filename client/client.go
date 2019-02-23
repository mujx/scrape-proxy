package main

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"nanomsg.org/go/mangos/v2"
	"nanomsg.org/go/mangos/v2/protocol/respondent"
	_ "nanomsg.org/go/mangos/v2/transport/all"

	"github.com/satori/go.uuid"

	log "github.com/sirupsen/logrus"

	utils "github.com/mujx/scrape-proxy/utils"
)

// CLI configuration options.
var (
	proxyUrl   = kingpin.Flag("proxy-url", "The server to connect to.").Default("tcp://127.0.0.1:5050").String()
	remoteFQDN = kingpin.Flag("remote-fqdn", "FQDN to forward the scrape requests.").Default("localhost").String()
)

func HandleScrapeRequests(url string, name string, remoteFQDN string) {
	var sock mangos.Socket
	var err error
	var msg []byte

	if sock, err = respondent.NewSocket(); err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Error("cannot get new respondent socket")
		return
	}

	if err = sock.Dial(url); err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Error("cannot dial on respondent socket")
		return
	}

	for {
		if msg, err = sock.Recv(); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("failed to call recv")
			return
		}

		var request utils.SurveyRequest
		if err := json.Unmarshal(msg, &request); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("failed to parse request")
			return
		}

		var response utils.SurveyResponse
		response.Id = name
		response.Errors = make(map[string]string)
		response.Payload = make(map[string]string)

		remoteURI, ok := request.ScrapeRequests[name]

		// The server sent a scrape request to this client.
		if ok {
			uri := strings.Replace(remoteURI, response.Id, remoteFQDN, -1)

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
		}

		value, err := json.Marshal(response)
		if err != nil {
			log.WithFields(log.Fields{
				"msg": string(err.Error()),
			}).Error("failed to serialize response")
			return
		}

		if err = sock.Send([]byte(value)); err != nil {
			log.WithFields(log.Fields{
				"error": string(err.Error()),
			}).Error("cannot send response")
			return
		}
	}
}

func main() {
	kingpin.Parse()

	utils.InitLogger()

	clientName := uuid.NewV4()

	log.WithFields(log.Fields{
		"proxyUrl":   *proxyUrl,
		"clientName": clientName,
	}).Info("scrape-proxy client started")

	for {
		HandleScrapeRequests(*proxyUrl, clientName.String(), *remoteFQDN)
		time.Sleep(time.Second)
	}
}
