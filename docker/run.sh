#!/bin/sh

if [ ${RUN_MODE} == "server" ] ; then
  exec /app/server \
      --survey.timeout=${SERVER_SURVEY_TIMEOUT} \
      --client.listen-address=${SERVER_CLIENT_LISTEN_ADDR} \
      --web.listen-address=${SERVER_WEB_LISTEN_ADDR}

elif [ ${RUN_MODE} == "client" ] ; then
  exec /app/client --proxy-url=${CLIENT_PROXY_ENDPOINT} --remote-fqdn=${CLIENT_REMOTE_FQDN}
fi
