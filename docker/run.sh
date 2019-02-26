#!/bin/sh

if [ ${RUN_MODE} == "server" ] ; then
  exec /app/server \
      --pull-url=${SERVER_PULL_URL} \
      --push-url=${SERVER_PUSH_URL} \
      --timeout=${SERVER_TIMEOUT} \
      --web-url=${SERVER_WEB_URL}

elif [ ${RUN_MODE} == "client" ] ; then
  exec /app/client \
      --pull-url=${CLIENT_PULL_URL} \
      --push-url=${CLIENT_PUSH_URL} \
      --remote-fqdn=${CLIENT_REMOTE_FQDN} \
      --heartbeat=${CLIENT_HEARTBEAT}
fi
