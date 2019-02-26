#!/bin/sh

if [ ${RUN_MODE} == "server" ] ; then
  exec /app/server \
      --pull-url=${SERVER_PULL_URL} \
      --push-url=${SERVER_PUSH_URL} \
      --timeout=${SERVER_TIMEOUT} \
      --log-level=${SERVER_LOG_LEVEL} \
      --web-url=${SERVER_WEB_URL}

elif [ ${RUN_MODE} == "client" ] ; then
  exec /app/client \
      --pull-url=${CLIENT_PULL_URL} \
      --push-url=${CLIENT_PUSH_URL} \
      --remote-fqdn=${CLIENT_REMOTE_FQDN} \
      --log-level=${CLIENT_LOG_LEVEL} \
      --heartbeat=${CLIENT_HEARTBEAT}
fi
